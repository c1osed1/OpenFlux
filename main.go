package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	godebug "runtime/debug"
	"strconv"
	"strings"

	_ "github.com/wlynxg/anet"
	"universal-bypass-tool/socks5"
	"universal-bypass-tool/transport"
	"universal-bypass-tool/transport/cupsonline"
	"universal-bypass-tool/transport/oneme"
	"universal-bypass-tool/transport/yandex"
	"universal-bypass-tool/tunnel"
	"universal-bypass-tool/utils"
)

var (
	globalDocUrl string
	maxToken     string
	maxUid       string
	localIP      string
)

func parseUIDs(s string) []int64 {
	var out []int64
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil || n == 0 {
			log.Fatalf("invalid maxUid %q", p)
		}
		out = append(out, n)
	}
	return out
}

func main() {
	fmt.Print("written by p1neappleXpress\n")

	exitNode := flag.Bool("exit-node", false, "Run as exit node")
	client := flag.Bool("client", false, "Run as client")
	debug := flag.Bool("debug", false, "Enable verbose debug logging (no contact/phone dump)")
	socksAddr := flag.String("socks5", ":1080", "SOCKS5 address")
	transportType := flag.String("transport", "yandex", "Transport type (yandex, vyandex, oneme, cupsonline)")
	mode := flag.String("mode", "proxy", "Exit-node mode: proxy (default, works everywhere) or raw (Linux only, needs root)")
	flag.StringVar(&globalDocUrl, "url", "http://#", "Document URL / cups room list")
	flag.StringVar(&maxToken, "maxToken", "", "MAX web auth token")
	flag.StringVar(&maxUid, "maxUid", "", "MAX callee user id (client). Comma-separated for failover")
	flag.StringVar(&localIP, "local-ip", "", "Egress IP for exit node (raw mode only, scoped RST drop)")
	bindIP := flag.String("bind-ip", "", "Exit-node source IPv4 (multi-IP hosts, proxy mode)")
	callDelay := flag.Int("call-delay", 3, "Seconds to wait before MAX outgoing call")
	maxPayload := flag.String("max-payload", "ice", "MAX payload path: ice (signaling injection) or dc (WebRTC DataChannel)")
	channel := flag.String("channel", "", "Yandex cursor channel so two tunnels can share one doc")
	encryptionKeyFile := flag.String("encryption-key-file", "",
		"Optional: encrypt the transport with AES-256-GCM using a shared secret read from this file. "+
			"Both peers must use the same secret; unset means unencrypted, unchanged behavior")
	flag.Parse()

	exitMode, err := tunnel.ParseExitMode(*mode)
	if err != nil {
		log.Fatalf("--mode: %v", err)
	}

	if *exitNode && exitMode == tunnel.ExitModeRaw && localIP != "" {
		tunnel.SetLocalIP(localIP)
	}

	// The exit node often runs on a tiny VPS; keep the heap tight under load
	// (GC aggressively). Set GOMEMLIMIT in the environment for a hard soft-cap.
	if *exitNode {
		godebug.SetGCPercent(20)
	}

	if !*exitNode && !*client {
		flag.Usage()
		os.Exit(1)
	}

	if *debug {
		utils.EnableDebug()
		oneme.SetVerbose(true)
	}
	if *bindIP != "" {
		tunnel.SetBindIP(*bindIP)
		log.Printf("Bind IP: %s", *bindIP)
	}

	icePayload := true
	switch strings.ToLower(*maxPayload) {
	case "ice":
		icePayload = true
	case "dc":
		icePayload = false
	default:
		log.Fatalf("unknown --max-payload %q (use ice or dc)", *maxPayload)
	}

	log.Printf("=== OpenFlux ===")
	log.Printf("Mode: %s", map[bool]string{true: "EXIT NODE", false: "CLIENT"}[*exitNode])
	log.Printf("Transport: %s", *transportType)
	if *exitNode {
		log.Printf("Exit mode: %s", exitMode.String())
	}
	if *channel != "" {
		log.Printf("Yandex channel: %s", *channel)
	}

	config := transport.DefaultConfig()
	var inner transport.Transport

	switch *transportType {
	case "vyandex":
		inner = yandex.NewYandexVolgaTransport(globalDocUrl, config)
	case "yandex":
		inner = yandex.NewYandexDocsTransport(globalDocUrl, config, *channel)
	case "oneme":
		callees := parseUIDs(maxUid)
		inner = oneme.NewOneMeTransport(*exitNode, maxToken, callees, config, *callDelay, icePayload)
	case "cupsonline":
		inner = cupsonline.NewCupsonlineTransport(globalDocUrl, config, !*exitNode)
	default:
		log.Fatalf("Unknown transport type: %s", *transportType)
	}

	if *encryptionKeyFile != "" {
		secretBytes, err := os.ReadFile(*encryptionKeyFile)
		if err != nil {
			log.Fatalf("Read encryption key file: %v", err)
		}
		context := *transportType
		if globalDocUrl != "" {
			context = globalDocUrl
		}
		encrypted, err := transport.NewEncryptedTransport(inner, strings.TrimSpace(string(secretBytes)), context, *exitNode)
		if err != nil {
			log.Fatalf("Configure encrypted transport: %v", err)
		}
		inner = encrypted
		log.Printf("Transport encryption: AES-256-GCM enabled")
	}

	trans := transport.NewCompressedTransport(inner)

	if err := trans.Start(); err != nil {
		log.Fatalf("Failed to start transport: %v", err)
	}

	tun := tunnel.NewTCPTunnelMode(trans, *exitNode, exitMode)

	if *exitNode {
		if exitMode == tunnel.ExitModeRaw {
			log.Printf("Running as EXIT NODE (raw mode)")
			if localIP != "" {
				log.Printf("! Run: sudo iptables -A OUTPUT -p tcp --tcp-flags RST RST -s %s -j DROP", localIP)
			} else {
				log.Printf("! Kernel RSTs would tear down tunnel connections. Prefer a scoped rule:")
				log.Printf("!   assign a dedicated alias IP, run with --local-ip <ip>, then:")
				log.Printf("!   sudo iptables -A OUTPUT -p tcp --tcp-flags RST RST -s <ip> -j DROP")
				log.Printf("! Host-wide fallback (drops ALL outbound RST; makes closed ports look filtered):")
				log.Printf("!   sudo iptables -A OUTPUT -p tcp --tcp-flags RST RST -j DROP")
			}
		} else {
			log.Printf("Running as EXIT NODE (proxy mode)")
		}
		select {}
	}

	log.Printf("Running as CLIENT (SOCKS5 on %s)", *socksAddr)
	socks5Server := socks5.NewSOCKS5Server(*socksAddr, tun)
	if err := socks5Server.Start(); err != nil {
		log.Fatalf("SOCKS5 listen failed: %v", err)
	}
}
