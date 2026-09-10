package yandex

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"universal-bypass-tool/transport"
	"universal-bypass-tool/utils"
)

type YandexDocsInfo struct {
	CookieStr   string
	Token       string
	DocID       string
	CallbackURL string
	UserID      string
	Origin      string
	Host        string
	WsURL       string
	Permissions map[string]interface{}
	OpenCmd     map[string]interface{}
}

type DocSession struct {
	Info       YandexDocsInfo
	Conn       *websocket.Conn
	WriteQueue chan []byte
	UserID     string
	writeMu    sync.Mutex
}

func (s *DocSession) safeWrite(messageType int, data []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.Conn.WriteMessage(messageType, data)
}

type YandexDocsTransport struct {
	*transport.BaseTransport

	url     string
	channel string
	session *DocSession

	userCounter  atomic.Int32
	baseUserID   string
	reconnectGen atomic.Uint32
}

func sanitizeChannel(ch string) string {
	var b strings.Builder
	for _, r := range ch {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if len(s) > 16 {
		s = s[:16]
	}
	return s
}

func NewYandexDocsTransport(url string, config transport.TransportConfig, channel string) *YandexDocsTransport {
	t := &YandexDocsTransport{
		BaseTransport: transport.NewBaseTransport(config),
		url:           url,
		channel:       sanitizeChannel(channel),
	}
	t.baseUserID = randUserID()
	return t
}

func (t *YandexDocsTransport) cursorValue(payload string) string {
	if t.channel == "" {
		return "18;" + payload
	}
	return "18;" + t.channel + ";" + payload
}

func (t *YandexDocsTransport) Start() error {
	if err := t.BaseTransport.Start(); err != nil {
		return err
	}

	t.baseUserID = randUserID()
	if t.channel != "" {
		utils.Debugf("[YDOCS] channel=%s", t.channel)
	}
	probeID := t.baseUserID + "001"
	if _, err := t.fetchDocInfo(t.url, probeID); err != nil {
		return fmt.Errorf("yandex doc unusable: %w", err)
	}
	go t.keepAliveLoop()
	t.connectToDoc(0)

	return nil
}

func (t *YandexDocsTransport) Send(data []byte) error {
	if !t.IsConnected() {
		return fmt.Errorf("transport not connected")
	}

	t.Mu.RLock()
	session := t.session
	t.Mu.RUnlock()

	if session == nil {
		return fmt.Errorf("no active session")
	}

	select {
	case session.WriteQueue <- data:
		t.RecordSend(len(data))
		return nil
	default:
		return fmt.Errorf("write queue full")
	}
}

func (t *YandexDocsTransport) connectToDoc(attempt int) {
	if !t.IsRunning() {
		return
	}

	utils.Debugf("[YDOCS] connectToDoc attempt ...")

	go func() {
		defer func() {
			if r := recover(); r != nil {
				utils.Debugf("[YDOCS] panic: %v", r)
				t.SetConnected(false)
				t.scheduleReconnect(attempt)
			}
		}()
		t.Mu.Lock()
		existingSession := t.session
		t.Mu.Unlock()

		var userID string
		if existingSession != nil {
			userID = existingSession.UserID
		} else {
			suffix := fmt.Sprintf("%03d", t.userCounter.Add(1)%1000)
			userID = t.baseUserID + suffix
		}

		info, err := t.fetchDocInfo(t.url, userID)
		if err != nil {
			log.Printf("[YDOCS] fetchDocInfo failed: %v", err)
			t.scheduleReconnect(attempt)
			return
		}

		dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
		headers := http.Header{}
		headers.Set("User-Agent", "Mozilla/5.0")
		headers.Set("Origin", info.Origin)
		headers.Set("Cookie", info.CookieStr)
		headers.Set("Host", info.Host)

		t.Mu.Lock()
		if existingSession != nil && existingSession.Conn != nil {
			existingSession.Conn.Close()
		}
		t.Mu.Unlock()

		conn, _, err := dialer.Dial(info.WsURL, headers)
		if err != nil {
			log.Printf("[YDOCS] websocket dial failed: %v", err)
			t.scheduleReconnect(attempt)
			return
		}
		conn.SetReadLimit(1 << 20)
		_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		conn.SetPongHandler(func(string) error {
			return conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		})

		writeQueue := make(chan []byte, t.GetConfig().MaxQueueSize)
		if existingSession != nil {
			writeQueue = existingSession.WriteQueue
		}

		session := &DocSession{
			Info:       info,
			Conn:       conn,
			WriteQueue: writeQueue,
			UserID:     userID,
		}

		t.Mu.Lock()
		t.session = session
		t.SetConnected(true)
		t.Mu.Unlock()

		if existingSession == nil {
			go t.writerLoop()
		}

		// Auth - use safeWrite
		auth1 := fmt.Sprintf(`40{"token":"%s"}`, info.Token)
		session.safeWrite(websocket.TextMessage, []byte(auth1))

		authData := map[string]interface{}{
			"type": "auth", "docid": info.DocID, "token": "fghhfgsjdgfjs",
			"user": map[string]interface{}{"id": userID}, "editorType": 0,
			"lastOtherSaveTime": -1, "permissions": info.Permissions,
			"openCmd": info.OpenCmd, "coEditingMode": "fast", "jwtOpen": info.Token,
		}
		messagePart, _ := json.Marshal([]interface{}{"message", authData})
		session.safeWrite(websocket.TextMessage, []byte(fmt.Sprintf("42%s", string(messagePart))))

		for t.IsRunning() {
			_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
			_, message, err := conn.ReadMessage()
			if err != nil {
				log.Printf("[YDOCS] websocket read: %v", err)
				t.SetConnected(false)
				t.scheduleReconnect(attempt)
				return
			}
			t.handleMessage(session, message)
		}
	}()
}

func (t *YandexDocsTransport) writerLoop() {
	for t.IsRunning() {
		t.Mu.Lock()
		session := t.session
		t.Mu.Unlock()

		if session == nil || session.Conn == nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}

		select {
		case packet := <-session.WriteQueue:
			payload := base64.StdEncoding.EncodeToString(packet)
			msg := fmt.Sprintf(`42["message",{"type":"cursor","cursor":"%s"}]`, t.cursorValue(payload))
			if err := session.safeWrite(websocket.TextMessage, []byte(msg)); err != nil {
				log.Printf("[YDOCS] write: %v", err)
			}
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (t *YandexDocsTransport) keepAliveLoop() {
	ticker := time.NewTicker(t.GetConfig().KeepAliveInterval)
	defer ticker.Stop()
	keepAliveMsg := fmt.Sprintf(`42["message",{"type":"cursor","cursor":"%s"}]`, t.cursorValue("---KA---"))

	for t.IsRunning() {
		<-ticker.C
		t.Mu.Lock()
		session := t.session
		t.Mu.Unlock()

		if session != nil && session.Conn != nil {
			if err := session.safeWrite(websocket.TextMessage, []byte(keepAliveMsg)); err != nil {
				log.Printf("[YDOCS] keep-alive: %v", err)
				t.SetConnected(false)
			}
		}
	}
}

func (t *YandexDocsTransport) handleMessage(session *DocSession, data []byte) {
	text := string(data)

	if strings.Contains(text, "---KA---") {
		return
	}

	// Socket.IO ping - respond with pong (use safeWrite)
	if text == "2" {
		if session != nil && session.Conn != nil {
			session.safeWrite(websocket.TextMessage, []byte("3"))
		}
		return
	}
	if text == "3" {
		return
	}

	if strings.Contains(text, "saveChanges") || strings.Contains(text, "cursor") {
		base64Str := t.extractBase64String(text)
		if base64Str == "" {
			return
		}

		decoded, err := base64.StdEncoding.DecodeString(base64Str)
		if err != nil {
			utils.Debugf("[YDOCS] Base64 decode error: %v", err)
			return
		}

		t.RecordReceive(len(decoded))
		t.CallReceive(decoded)
	}
}

func (t *YandexDocsTransport) extractBase64String(response string) string {
	if t.channel == "" && strings.Contains(response, "saveChanges") {
		marker := `"excelAdditionalInfo":"`
		left := strings.Index(response, marker) + len(marker)
		if left < len(marker) {
			return ""
		}
		right := strings.Index(response[left:], `"`)
		if right == -1 {
			return ""
		}
		return response[left : left+right]
	}

	if t.channel != "" {
		needle := `"cursor":"18;` + t.channel + `;`
		left := strings.Index(response, needle)
		if left < 0 {
			return ""
		}
		left += len(needle)
		right := strings.Index(response[left:], `"`)
		if right <= 0 {
			return ""
		}
		return response[left : left+right]
	}

	re := regexp.MustCompile(`"cursor":"18;([^"]+)"`)
	matches := re.FindStringSubmatch(response)
	if len(matches) < 2 {
		return ""
	}
	payload := matches[1]
	if strings.Contains(payload, ";") {
		return ""
	}
	return payload
}

func (t *YandexDocsTransport) scheduleReconnect(attempt int) {
	if !t.IsRunning() || attempt >= t.GetConfig().MaxReconnectAttempts {
		return
	}

	gen := t.reconnectGen.Add(1)
	t.RecordReconnect()
	delay := t.GetConfig().ReconnectDelay
	if delay <= 0 {
		delay = time.Second
	}
	for i := 0; i < attempt && delay < 15*time.Second; i++ {
		delay = time.Duration(float64(delay) * t.GetConfig().ReconnectMultiplier)
		if delay > 15*time.Second {
			delay = 15 * time.Second
		}
	}
	log.Printf("[YDOCS] reconnect in %s (attempt %d)", delay, attempt+1)
	time.Sleep(delay)
	if t.reconnectGen.Load() != gen {
		return
	}
	t.connectToDoc(attempt + 1)
}

func (t *YandexDocsTransport) fetchDocInfo(url, userID string) (YandexDocsInfo, error) {
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return nil },
		Timeout:       30 * time.Second,
	}

	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := client.Do(req)
	if err != nil {
		return YandexDocsInfo{}, err
	}
	defer resp.Body.Close()

	htmlBytes, _ := io.ReadAll(resp.Body)
	html := string(htmlBytes)

	var cookies []string
	for _, c := range resp.Cookies() {
		cookies = append(cookies, fmt.Sprintf("%s=%s", c.Name, c.Value))
	}

	re := regexp.MustCompile(`<script[^>]*id="client-config"[^>]*>(.*?)</script>`)
	matches := re.FindStringSubmatch(html)
	if len(matches) < 2 {
		return YandexDocsInfo{}, fmt.Errorf("config not found")
	}

	var config map[string]interface{}
	if err := json.Unmarshal([]byte(matches[1]), &config); err != nil {
		return YandexDocsInfo{}, fmt.Errorf("client-config json: %w", err)
	}
	officeAction, ok := asMap(config["officeActionData"])
	if !ok {
		return YandexDocsInfo{}, fmt.Errorf("officeActionData missing (need legacy OnlyOffice editor, not Volga/WOPI)")
	}

	editorConfigRaw, ok := asMap(officeAction["editor_config"])
	if !ok {
		return YandexDocsInfo{}, fmt.Errorf("editor_config missing (need legacy OnlyOffice editor, not Volga/WOPI)")
	}

	balancerURL, ok := officeAction["balancer_url"].(string)
	if !ok || balancerURL == "" {
		return YandexDocsInfo{}, fmt.Errorf("balancer_url missing")
	}
	host := strings.TrimPrefix(balancerURL, "https://")
	document, ok := asMap(editorConfigRaw["document"])
	if !ok {
		return YandexDocsInfo{}, fmt.Errorf("document config missing")
	}

	token, _ := editorConfigRaw["token"].(string)
	docKey, _ := document["key"].(string)
	if token == "" || docKey == "" {
		return YandexDocsInfo{}, fmt.Errorf("token or document key empty")
	}

	perms, _ := document["permissions"].(map[string]interface{})
	if perms == nil {
		perms = make(map[string]interface{})
	}

	return YandexDocsInfo{
		CookieStr:   strings.Join(cookies, "; "),
		Token:       token,
		DocID:       docKey,
		Origin:      balancerURL,
		Host:        host,
		WsURL:       fmt.Sprintf("wss://%s/2024.1.1-375/doc/%s/c/?EIO=4&transport=websocket", host, docKey),
		Permissions: perms,
		OpenCmd: map[string]interface{}{
			"c":      "open",
			"id":     docKey,
			"userid": userID,
			"format": document["fileType"],
			"url":    document["url"],
			"title":  document["title"],
			"lcid":   25,
		},
	}, nil
}

func asMap(v interface{}) (map[string]interface{}, bool) {
	m, ok := v.(map[string]interface{})
	return m, ok && m != nil
}

func randUserID() string {
	return fmt.Sprintf("%010d", rand.New(rand.NewSource(time.Now().UnixNano())).Intn(1000000000))
}
