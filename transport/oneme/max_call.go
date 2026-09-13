package oneme

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v3"
)

func (h *CallHandler) SetOnConnected(cb func())     { h.onConnected = cb }
func (h *CallHandler) SetDCInbound(cb func([]byte)) { h.dcInbound = cb }

func (h *CallHandler) Send(data []byte) {
	if h.icePayload {
		h.writeICEPayload(data)
		return
	}
	h.mu.Lock()
	dc := h.dc
	h.mu.Unlock()
	if dc == nil || dc.ReadyState() != webrtc.DataChannelStateOpen {
		return
	}
	_ = dc.Send(data)
}

func (h *CallHandler) SignalingUp() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conn != nil
}

func (h *CallHandler) PayloadReady() bool {
	if h.icePayload {
		return h.SignalingUp()
	}
	h.mu.Lock()
	dc := h.dc
	h.mu.Unlock()
	return dc != nil && dc.ReadyState() == webrtc.DataChannelStateOpen
}

func (h *CallHandler) readLoop() {
	defer func() {
		if r := recover(); r != nil {
			logError("recovered in CallHandler.readLoop: %v", r)
		}
	}()
	logInfo("[%s] Signaling connected", h.tag)
	for {
		_, message, err := h.conn.ReadMessage()
		if err != nil {
			logError("[%s] Signaling disconnected: %v", h.tag, err)
			h.signalReconnect()
			return
		}
		text := string(message)

		if strings.Contains(text, "accepted-call") {
			logInfo("[%s] Call accepted", h.tag)
			h.callAccepted = true
			continue
		}

		if text == "ping" {
			h.mu.Lock()
			if h.conn != nil {
				h.conn.WriteMessage(websocket.TextMessage, []byte("pong"))
			}
			h.mu.Unlock()
			continue
		}
		if len(text) < 10 {
			continue
		}
		var data map[string]interface{}
		if json.Unmarshal([]byte(text), &data) != nil {
			continue
		}
		if t, _ := data["type"].(string); t == "response" {
			continue
		}
		if t, _ := data["type"].(string); t == "error" {
			logError("[%s] SIGNALING ERROR: %v", h.tag, data["message"])
			continue
		}

		if pid, ok := data["participantId"].(float64); ok {
			h.remoteID = int64(pid)
		}
		go h.msgHandler(text)
	}
}

func (h *CallHandler) closeCall() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conn != nil {
		h.conn.Close()
		h.conn = nil
	}
	if h.pc != nil {
		h.pc.Close()
		h.pc = nil
	}
	h.dc = nil
	h.acceptSent = false
	h.callAccepted = false
	h.hasRemoteDesc = false
	h.localID = 0
	h.pendingCandidates = nil
}

func (h *CallHandler) signalReconnect() {
	if h.role == "caller" {
		select {
		case h.reconnectCh <- struct{}{}:
		default:
		}
	} else {
		logError("[%s] Receiver signaling died, waiting for next call", h.tag)
		h.closeCall()
	}
}

func (h *CallHandler) sendAcceptCall() {
	if h.acceptSent {
		return
	}
	h.acceptSent = true
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conn == nil {
		return
	}
	msg := fmt.Sprintf(`{"command":"accept-call","sequence":%d,"mediaSettings":{"isAudioEnabled":true,"isVideoEnabled":false,"isScreenSharingEnabled":false,"isFastScreenSharingEnabled":false,"isAudioSharingEnabled":false,"isAnimojiEnabled":false}}`, h.seq)
	h.seq++
	h.conn.WriteMessage(websocket.TextMessage, []byte(msg))
	logInfo("[%s] Accept-call sent", h.tag)
}

func (h *CallHandler) createPeerConnection(convParams map[string]interface{}) {
	logInfo("[%s] Creating PeerConnection...", h.tag)
	turn, ok := convParams["turn"].(map[string]interface{})
	if !ok {
		logError("[%s] conversationParams.turn missing", h.tag)
		return
	}
	stun, _ := convParams["stun"].(map[string]interface{})
	if stun == nil {
		stun = map[string]interface{}{}
	}
	var stunURLs, turnURLs []string
	if urls, ok := stun["urls"].([]interface{}); ok && len(urls) > 0 {
		stunURLs = []string{urls[0].(string)}
	}
	if urls, ok := turn["urls"].([]interface{}); ok {
		for _, u := range urls {
			turnURLs = append(turnURLs, u.(string))
		}
	}
	username, _ := turn["username"].(string)
	credential, _ := turn["credential"].(string)
	logDebug("[%s] STUN: %v  TURN: %v", h.tag, stunURLs, turnURLs)

	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs:           turnURLs,
				Username:       username,
				Credential:     credential,
				CredentialType: webrtc.ICECredentialTypePassword,
			},
		},
		ICETransportPolicy: webrtc.ICETransportPolicyRelay,
	}

	pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		logError("[%s] ERROR creating PC: %v", h.tag, err)
		return
	}
	h.pc = pc

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			logDebug("[%s] ICE gathering complete", h.tag)
			return
		}
		jsonC, _ := json.Marshal(c.ToJSON())
		logDebug("[%s] Local ICE: %s", h.tag, string(jsonC))
		h.sendRealICE(string(jsonC))
	})
	pc.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
		logInfo("[%s] ICE: %s", h.tag, s.String())
	})
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		logInfo("[%s] Connection: %s", h.tag, s.String())
		if s == webrtc.PeerConnectionStateConnected && h.onConnected != nil {
			logInfo("[%s] *** CONNECTED! ***", h.tag)
			h.onConnected()
		}
		if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateClosed {
			if h.role == "caller" && !h.icePayload {
				h.signalReconnect()
			}
		}
	})
	pc.OnSignalingStateChange(func(s webrtc.SignalingState) {
		logDebug("[%s] Signaling: %s", h.tag, s.String())
	})
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		dcID := uint16(0)
		if dc.ID() != nil {
			dcID = *dc.ID()
		}
		logInfo("[%s] Remote DC: %s (id=%d)", h.tag, dc.Label(), dcID)
		h.mu.Lock()
		h.dc = dc
		h.mu.Unlock()
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			logDebug("[%s] DC recv %d bytes", h.tag, len(msg.Data))
			if h.dcInbound != nil {
				h.dcInbound(msg.Data)
			}
		})
	})

	ordered := true
	maxRetransmits := uint16(0)
	dc, err := pc.CreateDataChannel("x", &webrtc.DataChannelInit{
		Ordered: &ordered, MaxRetransmits: &maxRetransmits,
	})
	if err != nil {
		logError("[%s] ERROR creating DC: %v", h.tag, err)
		return
	}
	if dc == nil {
		logError("[%s] DC is nil", h.tag)
		return
	}
	h.mu.Lock()
	h.dc = dc
	h.mu.Unlock()
	dc.OnOpen(func() { logInfo("[%s] DC opened", h.tag) })
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		logDebug("[%s] DC recv %d bytes", h.tag, len(msg.Data))
		if h.dcInbound != nil {
			h.dcInbound(msg.Data)
		}
	})
}

func (h *CallHandler) sendSDP(sdp string, sdpType string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conn == nil {
		return
	}
	escaped, _ := json.Marshal(sdp)
	msg := fmt.Sprintf(`{"command":"transmit-data","sequence":%d,"participantId":%d,"data":{"sdp":{"type":"%s","sdp":%s},"animojiVersion":1},"participantType":"USER"}`,
		h.seq, h.localID, sdpType, string(escaped))
	h.seq++
	logInfo("[%s] Sent SDP %s (%d bytes)", h.tag, sdpType, len(sdp))
	if err := h.conn.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
		logError("[%s] SDP write: %v", h.tag, err)
	}
}

func (h *CallHandler) writeICEPayload(payload []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conn == nil {
		return
	}
	escaped, _ := json.Marshal(base64.StdEncoding.EncodeToString(payload))
	msg := fmt.Sprintf(`{"command":"transmit-data","sequence":%d,"participantId":%d,"data":{"candidate":{"candidate":%s}},"participantType":"USER"}`,
		h.seq, h.localID, string(escaped))
	h.seq++
	h.conn.WriteMessage(websocket.TextMessage, []byte(msg))
}

func (h *CallHandler) sendRealICE(candidateJSON string) {
	if h.icePayload {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conn == nil {
		return
	}
	var ice struct {
		Candidate string `json:"candidate"`
	}
	json.Unmarshal([]byte(candidateJSON), &ice)
	escaped, _ := json.Marshal(ice.Candidate)
	msg := fmt.Sprintf(`{"command":"transmit-data","sequence":%d,"participantId":%d,"data":{"candidate":{"candidate":%s}},"participantType":"USER"}`,
		h.seq, h.localID, string(escaped))
	h.seq++
	h.conn.WriteMessage(websocket.TextMessage, []byte(msg))
}

func (h *CallHandler) addICECandidate(c map[string]interface{}) {
	if h.pc == nil {
		return
	}
	candidateStr, _ := c["candidate"].(string)
	sdpMid, _ := c["sdpMid"].(string)
	sdpMLineIndex := uint16(0)
	if idx, ok := c["sdpMLineIndex"].(float64); ok {
		sdpMLineIndex = uint16(idx)
	}
	if err := h.pc.AddICECandidate(webrtc.ICECandidateInit{
		Candidate: candidateStr, SDPMid: &sdpMid, SDPMLineIndex: &sdpMLineIndex,
	}); err != nil {
		logError("[%s] ICE error: %v", h.tag, err)
	}
}

func (h *CallHandler) bufferOrAddICE(c map[string]interface{}) {
	if !h.hasRemoteDesc {
		h.pendingCandidates = append(h.pendingCandidates, c)
		logDebug("[%s] Buffered ICE (%d total)", h.tag, len(h.pendingCandidates))
	} else {
		h.addICECandidate(c)
	}
}

func (h *CallHandler) flushPendingCandidates() {
	if len(h.pendingCandidates) == 0 {
		return
	}
	logDebug("[%s] Flushing %d buffered ICE", h.tag, len(h.pendingCandidates))
	for _, c := range h.pendingCandidates {
		h.addICECandidate(c)
	}
	h.pendingCandidates = nil
}

func (h *CallHandler) handleSDP(sdpType string, sdpStr string) {
	if h.pc == nil {
		return
	}
	logInfo("[%s] handleSDP: %s (%d bytes)", h.tag, sdpType, len(sdpStr))

	switch sdpType {
	case "offer":
		logInfo("[%s] Setting remote offer...", h.tag)
		if err := h.pc.SetRemoteDescription(webrtc.SessionDescription{
			Type: webrtc.SDPTypeOffer, SDP: sdpStr,
		}); err != nil {
			logError("[%s] ERROR: %v", h.tag, err)
			return
		}
		time.Sleep(300 * time.Millisecond)
		h.hasRemoteDesc = true
		h.flushPendingCandidates()

		logInfo("[%s] Creating answer...", h.tag)
		answer, err := h.pc.CreateAnswer(nil)
		if err != nil {
			logError("[%s] ERROR: %v", h.tag, err)
			return
		}
		if err := h.pc.SetLocalDescription(answer); err != nil {
			logError("[%s] SetLocalDescription: %v", h.tag, err)
			return
		}
		h.sendSDP(answer.SDP, "answer")

	case "answer":
		logInfo("[%s] Setting remote answer...", h.tag)
		if err := h.pc.SetRemoteDescription(webrtc.SessionDescription{
			Type: webrtc.SDPTypeAnswer, SDP: sdpStr,
		}); err != nil {
			logError("[%s] ERROR: %v", h.tag, err)
			return
		}
		h.hasRemoteDesc = true
		h.flushPendingCandidates()
	}
}

func (h *CallHandler) waitAccepted(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if h.callAccepted || h.icePayload {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return h.callAccepted || h.icePayload
}

func startOutgoingCall(client *MaxClient, callees []int64, callDelaySec int, icePayload bool) *CallHandler {
	h := &CallHandler{tag: "CALLER", role: "caller", icePayload: icePayload, callees: callees}
	h.seq = 1
	h.reconnectCh = make(chan struct{}, 1)
	h.msgHandler = func(text string) {
		var data map[string]interface{}
		json.Unmarshal([]byte(text), &data)

		if h.localID == 0 {
			if conv, ok := data["conversation"].(map[string]interface{}); ok {
				if parts, ok := conv["participants"].([]interface{}); ok {
					for _, p := range parts {
						part := p.(map[string]interface{})
						roles, _ := part["roles"].([]interface{})
						isCreator := false
						for _, r := range roles {
							if r.(string) == "CREATOR" {
								isCreator = true
							}
						}
						if !isCreator {
							h.localID = int64(part["id"].(float64))
							logInfo("[%s] Local ID: %d", h.tag, h.localID)
						}
					}
				}
			}
		}

		if cp, ok := data["conversationParams"].(map[string]interface{}); ok {
			logInfo("[%s] conversationParams - creating offer", h.tag)
			if !h.waitAccepted(45 * time.Second) {
				logError("[%s] Timed out waiting for accept, reconnecting", h.tag)
				h.signalReconnect()
				return
			}
			h.createPeerConnection(cp)
			time.Sleep(200 * time.Millisecond)
			if h.pc == nil {
				logError("[%s] PeerConnection missing", h.tag)
				return
			}
			offer, err := h.pc.CreateOffer(nil)
			if err != nil {
				logError("[%s] ERROR: %v", h.tag, err)
				return
			}
			if !h.icePayload {
				if err := h.pc.SetLocalDescription(offer); err != nil {
					logError("[%s] SetLocalDescription: %v", h.tag, err)
					return
				}
				h.sendSDP(offer.SDP, "offer")
			}
			return
		}
		d, _ := data["data"].(map[string]interface{})
		if d == nil {
			return
		}
		if sdp, ok := d["sdp"].(map[string]interface{}); ok {
			sdpType, _ := sdp["type"].(string)
			if sdpType == "answer" {
				if h.icePayload {
					return
				}
				h.handleSDP(sdpType, sdp["sdp"].(string))
			}
			return
		}
		if c, ok := d["candidate"].(map[string]interface{}); ok {
			if h.icePayload {
				candidateStr, _ := c["candidate"].(string)
				decode, _ := base64.StdEncoding.DecodeString(candidateStr)
				if h.dcInbound != nil {
					h.dcInbound(decode)
				}
			} else {
				h.bufferOrAddICE(c)
			}
		}
	}

	go func() {
		if callDelaySec > 0 {
			logInfo("[CALLER] Waiting %ds before first call", callDelaySec)
			time.Sleep(time.Duration(callDelaySec) * time.Second)
		}
		backoff := time.Second
		attempt := 0
		for {
			if len(callees) == 0 {
				logError("[CALLER] No callee UIDs")
				return
			}
			calleeID := callees[attempt%len(callees)]
			logInfo("[CALLER] Calling %d (attempt %d)", calleeID, attempt+1)
			h.closeCall()
			h.mu.Lock()
			h.seq = 1
			h.mu.Unlock()

			resp, err := client.invoke(78, map[string]interface{}{
				"conversationId": genUUID(),
				"calleeIds":      []int64{calleeID},
				"internalParams": fmt.Sprintf(`{"deviceId":"%s","sdkVersion":"2.8.9","clientAppKey":"CNHIJPLGDIHBABABA","platform":"WEB","protocolVersion":5,"domainId":"","capabilities":"2A03F"}`, client.deviceID),
				"isVideo":        false,
			})
			if err != nil {
				logError("[CALLER] Start-call error: %v", err)
				time.Sleep(backoff)
				backoff = nextBackoff(backoff)
				attempt++
				continue
			}
			var payload map[string]interface{}
			json.Unmarshal(resp.Payload, &payload)
			paramsStr, _ := payload["internalCallerParams"].(string)
			var params InternalCallerParams
			json.Unmarshal([]byte(paramsStr), &params)
			if params.Endpoint == "" || !strings.HasPrefix(params.Endpoint, "ws") {
				logError("[CALLER] Start-call returned no websocket endpoint, retrying")
				time.Sleep(backoff)
				backoff = nextBackoff(backoff)
				attempt++
				continue
			}
			endpoint := params.Endpoint + "&platform=WEB&appVersion=1.1&version=5&device=browser&capabilities=2A03F&clientType=ONE_ME&tgt=start"
			conn, _, err := websocket.DefaultDialer.Dial(endpoint, nil)
			if err != nil {
				logError("[CALLER] Dial error: %v, retrying...", err)
				time.Sleep(backoff)
				backoff = nextBackoff(backoff)
				attempt++
				continue
			}
			h.mu.Lock()
			h.conn = conn
			h.mu.Unlock()
			backoff = time.Second
			go h.readLoop()

			<-h.reconnectCh
			logInfo("[CALLER] Reconnecting...")
			attempt++
			time.Sleep(backoff)
			backoff = nextBackoff(backoff)
		}
	}()

	return h
}

func nextBackoff(d time.Duration) time.Duration {
	n := d * 2
	if n > 15*time.Second {
		return 15 * time.Second
	}
	return n
}

func startIncomingListener(client *MaxClient, icePayload bool) *CallHandler {
	h := &CallHandler{tag: "RECEIVER", role: "receiver", icePayload: icePayload}
	h.seq = 1
	h.msgHandler = func(text string) {
		var data map[string]interface{}
		json.Unmarshal([]byte(text), &data)

		if h.localID == 0 {
			if conv, ok := data["conversation"].(map[string]interface{}); ok {
				if parts, ok := conv["participants"].([]interface{}); ok {
					for _, p := range parts {
						part := p.(map[string]interface{})
						roles, _ := part["roles"].([]interface{})
						isCreator := false
						for _, r := range roles {
							if r.(string) == "CREATOR" {
								isCreator = true
							}
						}
						if isCreator {
							h.localID = int64(part["id"].(float64))
							logInfo("[%s] Local ID: %d", h.tag, h.localID)
						}
					}
				}
			}
		}

		if cp, ok := data["conversationParams"].(map[string]interface{}); ok {
			h.createPeerConnection(cp)
			return
		}
		d, _ := data["data"].(map[string]interface{})
		if d == nil {
			return
		}
		if sdp, ok := d["sdp"].(map[string]interface{}); ok {
			h.handleSDP(sdp["type"].(string), sdp["sdp"].(string))
			return
		}
		if c, ok := d["candidate"].(map[string]interface{}); ok {
			if h.icePayload {
				candidateStr, _ := c["candidate"].(string)
				decode, _ := base64.StdEncoding.DecodeString(candidateStr)
				if h.dcInbound != nil {
					h.dcInbound(decode)
				}
			} else {
				h.bufferOrAddICE(c)
			}
		}
	}

	client.SetEventCallback(func(p MaxPacket) {
		if p.Opcode == 137 {
			logInfo("[RECEIVER] Incoming call!")
			var payload map[string]interface{}
			json.Unmarshal(p.Payload, &payload)
			convID, _ := payload["conversationId"].(string)
			vcp, _ := payload["vcp"].(string)
			callDetails, err := decodeCallDetails(vcp)
			if err != nil {
				logError("[RECEIVER] Decode error: %v", err)
				return
			}

			endpoint := craftEndpoint(convID, callDetails)
			logInfo("[RECEIVER] Dialing signaling")
			logDebug("[RECEIVER] endpoint: %s", endpoint)

			conn, _, err := websocket.DefaultDialer.Dial(endpoint, nil)
			if err != nil {
				logError("[RECEIVER] Connect error: %v", err)
				return
			}
			h.mu.Lock()
			h.conn = conn
			h.mu.Unlock()
			go h.readLoop()
			go func() {
				time.Sleep(1 * time.Second)
				if !h.acceptSent {
					h.sendAcceptCall()
				}
			}()
		}
	})
	logInfo("[RECEIVER] Waiting for calls...")
	return h
}
