package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	_ "github.com/wlynxg/anet"
	"universal-bypass-tool/socks5"
	"universal-bypass-tool/transport"
	"universal-bypass-tool/transport/oneme"
	"universal-bypass-tool/transport/yandex"
	"universal-bypass-tool/tunnel"
	"universal-bypass-tool/utils"
)

var (
	globalDocUrl string
	maxToken     string
	maxUid       string
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

	exitNode := flag.Bool("exit-node", false, "Run as exit node (needs root)")
	client := flag.Bool("client", false, "Run as client")
	debug := flag.Bool("debug", false, "Enable verbose debug logging (no contact/phone dump)")
	socksAddr := flag.String("socks5", ":1080", "SOCKS5 address")
	transportType := flag.String("transport", "yandex", "Transport type (yandex, oneme)")
	flag.StringVar(&globalDocUrl, "url", "http://#", "Document URL for Yandex.Docs transport")
	flag.StringVar(&maxToken, "maxToken", "", "MAX web auth token")
	flag.StringVar(&maxUid, "maxUid", "", "MAX callee user id (client). Comma-separated for failover")
	bindIP := flag.String("bind-ip", "", "Exit-node source IPv4 (multi-IP hosts)")
	callDelay := flag.Int("call-delay", 3, "Seconds to wait before MAX outgoing call")
	maxPayload := flag.String("max-payload", "ice", "MAX payload path: ice (signaling injection) or dc (WebRTC DataChannel)")
	channel := flag.String("channel", "", "Yandex cursor channel so two tunnels can share one doc")
	flag.Parse()

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
	if *channel != "" {
		log.Printf("Yandex channel: %s", *channel)
	}

	config := transport.DefaultConfig()
	var trans transport.Transport

	switch *transportType {
	case "yandex":
		trans = transport.NewCompressedTransport(yandex.NewYandexDocsTransport(globalDocUrl, config, *channel))
	case "oneme":
		callees := parseUIDs(maxUid)
		trans = transport.NewCompressedTransport(oneme.NewOneMeTransport(*exitNode, maxToken, callees, config, *callDelay, icePayload))
	default:
		log.Fatalf("Unknown transport type: %s", *transportType)
	}

	if err := trans.Start(); err != nil {
		log.Fatalf("Failed to start transport: %v", err)
	}

	tun := tunnel.NewTCPTunnel(trans, *exitNode)

	if *exitNode {
		log.Printf("Running as EXIT NODE (needs root for raw socket)")
		log.Printf("! Run: sudo iptables -A OUTPUT -p tcp --tcp-flags RST RST -j DROP")
		select {}
	}

	log.Printf("Running as CLIENT (SOCKS5 on %s)", *socksAddr)
	socks5Server := socks5.NewSOCKS5Server(*socksAddr, tun)
	if err := socks5Server.Start(); err != nil {
		log.Fatalf("SOCKS5 listen failed: %v", err)
	}
}
