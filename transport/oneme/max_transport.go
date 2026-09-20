package oneme

import (
	"fmt"
	"strings"
	"time"

	"openflux/transport"
	"openflux/utils"
)

type OneMeTransport struct {
	b          *transport.BaseTransport
	token      string
	callees    []int64
	exit       bool
	callDelay  int
	icePayload bool

	oneMeClient MaxClient
	ch          *CallHandler
}

func (t *OneMeTransport) Receive(callback func([]byte)) {
	t.b.Receive(callback)
}

func (t *OneMeTransport) Stats() transport.TransportStats {
	return t.b.Stats()
}

func NewOneMeTransport(isExit bool, maxToken string, callees []int64, config transport.TransportConfig, callDelay int, icePayload bool) *OneMeTransport {
	return &OneMeTransport{
		b:          transport.NewBaseTransport(config),
		token:      strings.TrimSpace(maxToken),
		callees:    callees,
		exit:       isExit,
		callDelay:  callDelay,
		icePayload: icePayload,
	}
}

func (t *OneMeTransport) Start() error {
	utils.Debugf("creating max client ...")
	t.oneMeClient = *NewMaxClient()
	if err := t.oneMeClient.Connect(); err != nil {
		return fmt.Errorf("max connect: %w", err)
	}
	if err := t.oneMeClient.LoginByToken(t.token); err != nil {
		logError("[MAX] login: %v (continuing; start-call may still work)", err)
	}

	mode := "datachannel"
	if t.icePayload {
		mode = "ice-injection"
	}
	logInfo("[MAX] payload mode: %s", mode)

	if t.exit {
		utils.Debugf("configured ch for exit node")
		t.ch = startIncomingListener(&t.oneMeClient, t.icePayload)
	} else {
		if len(t.callees) == 0 {
			return fmt.Errorf("maxUid required in client mode")
		}
		utils.Debugf("configured ch for client mode")
		t.ch = startOutgoingCall(&t.oneMeClient, t.callees, t.callDelay, t.icePayload)
	}

	utils.Debugf("configured dc inbound")
	t.ch.dcInbound = func(data []byte) {
		t.b.RecordReceive(len(data))
		t.b.CallReceive(data)
	}

	if err := t.b.Start(); err != nil {
		return err
	}
	go t.logStats()
	return nil
}

func (t *OneMeTransport) logStats() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if !t.b.IsRunning() {
			return
		}
		s := t.b.Stats()
		logInfo("[MAX] stats sent=%dB recv=%dB pkts_out=%d pkts_in=%d ready=%v",
			s.BytesSent, s.BytesReceived, s.PacketsSent, s.PacketsRecv, t.IsConnected())
	}
}

func (t *OneMeTransport) Stop() error {
	return t.b.Stop()
}

func (t *OneMeTransport) IsConnected() bool {
	if t.ch == nil {
		return false
	}
	return t.ch.PayloadReady()
}

func (t *OneMeTransport) Send(data []byte) error {
	if t.ch == nil {
		return fmt.Errorf("max call not started")
	}
	t.ch.Send(data)
	t.b.RecordSend(len(data))
	return nil
}


