package transport

import (
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"openflux/utils"
)

// DirectConfig configures the DirectTransport.
//
// DirectTransport is a plain TCP byte-stream carrier between a client and an
// exit node. It carries already framed tunnel packets, so every record on the
// wire is exactly one length-prefixed frame:
//
//	[2-byte big-endian length][payload]
//
// It is intended to be wrapped by EncryptedTransport (AES-256-GCM) on both
// sides. main.go MUST refuse to start --transport=direct without a key.
//
// Ported from upstream (github.com/p1neappleXpress/OpenFlux); the netbind
// dependency is replaced by a plain net.Dialer so it drops into this fork
// unchanged.
type DirectConfig struct {
	// ListenAddr is the local address the exit node binds to.
	// Empty or ":0" means "any free port on all interfaces".
	// Set to 0.0.0.0:PORT when running as an exit node reachable from the
	// internet, so the socket is not loopback-only.
	ListenAddr string

	// DialAddr is the remote address the client dials. "host:port".
	DialAddr string

	// IsExit chooses listen-vs-dial. Exactly one of the two must be true on
	// each peer.
	IsExit bool

	// HandshakeTimeout bounds the initial TCP connect / accept.
	HandshakeTimeout time.Duration

	// ReadTimeout bounds a single Read on the wire. Zero disables the deadline
	// and relies on TCP keepalives.
	ReadTimeout time.Duration

	// KeepAliveInterval controls the TCP keepalive probe interval.
	KeepAliveInterval time.Duration

	// ReconnectMinDelay / ReconnectMaxDelay / ReconnectMultiplier control the
	// client reconnect backoff. The exit node does not reconnect; it accepts.
	ReconnectMinDelay   time.Duration
	ReconnectMaxDelay   time.Duration
	ReconnectMultiplier float64

	// MaxRecordBytes caps a single framed record (2-byte length prefix means
	// 65535 is the hard ceiling).
	MaxRecordBytes int
}

// DefaultDirectConfig returns conservative defaults.
func DefaultDirectConfig() DirectConfig {
	return DirectConfig{
		HandshakeTimeout:    15 * time.Second,
		ReadTimeout:         0, // rely on TCP keepalive
		KeepAliveInterval:   30 * time.Second,
		ReconnectMinDelay:   200 * time.Millisecond,
		ReconnectMaxDelay:   15 * time.Second,
		ReconnectMultiplier: 1.4,
		MaxRecordBytes:      65535,
	}
}

// DirectTransport is a minimal TCP carrier for already-framed tunnel records.
//
// Client mode dials DialAddr and reconnects on drop.
// Exit mode listens on ListenAddr and accepts a single active peer at a time.
//
// The transport does not encrypt, compress, or batch: wrap it with
// EncryptedTransport (and the codec) in main.go.
type DirectTransport struct {
	*BaseTransport

	config DirectConfig

	mu   sync.RWMutex
	conn net.Conn

	done chan struct{}
	once sync.Once

	writeMu sync.Mutex

	queue chan []byte

	listener net.Listener

	reconnects atomic.Uint64
	drops      atomic.Uint64

	bytesIn    atomic.Uint64
	bytesOut   atomic.Uint64
	recordsIn  atomic.Uint64
	recordsOut atomic.Uint64
}

// NewDirectTransport builds a DirectTransport from a base config and a
// transport.DirectConfig.
func NewDirectTransport(base TransportConfig, cfg DirectConfig) *DirectTransport {
	t := &DirectTransport{
		BaseTransport: NewBaseTransport(base),
		config:        cfg,
		done:          make(chan struct{}),
		queue:         make(chan []byte, base.MaxQueueSize),
	}
	utils.Debugf("[DIRECT] created: isExit=%v listen=%q dial=%q maxRecord=%d",
		cfg.IsExit, cfg.ListenAddr, cfg.DialAddr, cfg.MaxRecordBytes)
	return t
}

// Start launches the accept (exit) or dial (client) loop.
func (t *DirectTransport) Start() error {
	if err := t.BaseTransport.Start(); err != nil {
		return err
	}

	if t.config.IsExit {
		addr := t.config.ListenAddr
		if addr == "" {
			addr = "0.0.0.0:0"
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("direct: listen %s: %w", addr, err)
		}
		t.listener = ln
		utils.Debugf("[DIRECT] exit listening on %s (waiting for a client)", ln.Addr().String())
		go t.acceptLoop()
	} else {
		if t.config.DialAddr == "" {
			return fmt.Errorf("direct: DialAddr is empty")
		}
		utils.Debugf("[DIRECT] client: starting dial loop to %s", t.config.DialAddr)
		go t.dialLoop()
	}

	go t.writerLoop()
	return nil
}

// Stop closes the listener, the active connection and the outbound queue.
func (t *DirectTransport) Stop() error {
	t.once.Do(func() {
		close(t.done)
	})
	t.mu.Lock()
	if t.listener != nil {
		_ = t.listener.Close()
		t.listener = nil
	}
	if t.conn != nil {
		_ = t.conn.Close()
		t.conn = nil
	}
	t.mu.Unlock()
	t.SetConnected(false)
	return t.BaseTransport.Stop()
}

// Send enqueues an already-framed record for delivery.
func (t *DirectTransport) Send(data []byte) error {
	if !t.IsRunning() {
		return fmt.Errorf("direct: not running")
	}
	if len(data) == 0 || len(data) > t.config.MaxRecordBytes {
		return fmt.Errorf("direct: record size %d outside 1..%d", len(data), t.config.MaxRecordBytes)
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	select {
	case t.queue <- cp:
		return nil
	default:
		t.drops.Add(1)
		return fmt.Errorf("direct: queue full")
	}
}

// Receive installs the user callback.
func (t *DirectTransport) Receive(cb func([]byte)) {
	t.BaseTransport.Receive(cb)
}

// IsConnected reports whether a live TCP connection exists.
func (t *DirectTransport) IsConnected() bool {
	return t.BaseTransport.IsConnected()
}

// Stats returns counters plus reconnect/drop counts specific to DirectTransport.
func (t *DirectTransport) Stats() TransportStats {
	s := t.BaseTransport.Stats()
	s.Reconnects = t.reconnects.Load()
	s.Connected = t.IsConnected()
	return s
}

// Drops returns the number of Send calls dropped because the queue was full.
func (t *DirectTransport) Drops() uint64 { return t.drops.Load() }

// ---- exit mode ----

func (t *DirectTransport) acceptLoop() {
	for {
		select {
		case <-t.done:
			return
		default:
		}

		conn, err := t.listener.Accept()
		if err != nil {
			select {
			case <-t.done:
				return
			default:
			}
			utils.Debugf("[DIRECT] acceptLoop: Accept error: %v", err)
			continue
		}
		utils.Debugf("[DIRECT] acceptLoop: ACCEPTED %s", connDesc(conn))
		t.serveConn(conn)
	}
}

// ---- client mode ----

func (t *DirectTransport) dialLoop() {
	delay := t.config.ReconnectMinDelay
	if delay <= 0 {
		delay = 200 * time.Millisecond
	}
	attempt := 0
	for {
		select {
		case <-t.done:
			return
		default:
		}

		attempt++
		d := &net.Dialer{
			Timeout:   t.config.HandshakeTimeout,
			KeepAlive: t.config.KeepAliveInterval,
		}
		conn, err := d.Dial("tcp", t.config.DialAddr)
		if err != nil {
			utils.Debugf("[DIRECT] dialLoop: attempt #%d dial %s failed: %v", attempt, t.config.DialAddr, err)
		} else {
			utils.Debugf("[DIRECT] dialLoop: CONNECTED to %s (local=%s)", t.config.DialAddr, connDesc(conn))
			t.serveConn(conn)
			t.reconnects.Add(1)
			delay = t.config.ReconnectMinDelay
			if delay <= 0 {
				delay = 200 * time.Millisecond
			}
		}

		select {
		case <-t.done:
			return
		case <-time.After(delay):
		}
		delay = time.Duration(float64(delay) * t.config.ReconnectMultiplier)
		if delay > t.config.ReconnectMaxDelay {
			delay = t.config.ReconnectMaxDelay
		}
	}
}

// ---- shared conn lifecycle ----

func (t *DirectTransport) serveConn(conn net.Conn) {
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
		if t.config.KeepAliveInterval > 0 {
			_ = tc.SetKeepAlive(true)
			_ = tc.SetKeepAlivePeriod(t.config.KeepAliveInterval)
		}
	}

	t.mu.Lock()
	t.conn = conn
	t.mu.Unlock()
	t.SetConnected(true)

	defer func() {
		t.mu.Lock()
		if t.conn == conn {
			t.conn = nil
		}
		t.mu.Unlock()
		t.SetConnected(false)
		_ = conn.Close()
	}()

	buf := make([]byte, t.config.MaxRecordBytes)
	for {
		select {
		case <-t.done:
			return
		default:
		}

		if t.config.ReadTimeout > 0 {
			_ = conn.SetReadDeadline(time.Now().Add(t.config.ReadTimeout))
		}
		if _, err := io.ReadFull(conn, buf[:2]); err != nil {
			return
		}
		n := int(buf[0])<<8 | int(buf[1])
		if n == 0 || n > t.config.MaxRecordBytes {
			utils.Debugf("[DIRECT] serveConn: invalid record length %d (max %d), closing", n, t.config.MaxRecordBytes)
			return
		}
		if _, err := io.ReadFull(conn, buf[:n]); err != nil {
			return
		}

		pkt := make([]byte, n)
		copy(pkt, buf[:n])
		t.RecordReceive(n)
		t.bytesIn.Add(uint64(n))
		t.recordsIn.Add(1)
		t.CallReceive(pkt)
	}
}

// ---- writer ----

func (t *DirectTransport) writerLoop() {
	var pending []byte
	for {
		if pending == nil {
			select {
			case <-t.done:
				return
			case pkt := <-t.queue:
				pending = pkt
			}
		}

		t.mu.RLock()
		conn := t.conn
		t.mu.RUnlock()
		if conn == nil {
			select {
			case <-t.done:
				return
			case <-time.After(20 * time.Millisecond):
			}
			continue
		}

		if len(pending) > t.config.MaxRecordBytes {
			pending = nil
			continue
		}
		hdr := [2]byte{byte(len(pending) >> 8), byte(len(pending))}

		t.writeMu.Lock()
		_, err := conn.Write(hdr[:])
		if err == nil {
			_, err = conn.Write(pending)
		}
		t.writeMu.Unlock()

		if err != nil {
			utils.Debugf("[DIRECT] writerLoop: write failed for %s: %v (holding record)", connDesc(conn), err)
			select {
			case <-t.done:
				return
			case <-time.After(20 * time.Millisecond):
			}
			continue // keep pending; reconnect will bring up a new conn
		}
		t.RecordSend(len(pending))
		t.bytesOut.Add(uint64(len(pending)))
		t.recordsOut.Add(1)
		pending = nil
	}
}

// connDesc returns a short descriptor for a net.Conn for logging.
func connDesc(c net.Conn) string {
	if c == nil {
		return "<nil>"
	}
	local := "?"
	remote := "?"
	if a := c.LocalAddr(); a != nil {
		local = a.String()
	}
	if a := c.RemoteAddr(); a != nil {
		remote = a.String()
	}
	return fmt.Sprintf("%s->%s", local, remote)
}
