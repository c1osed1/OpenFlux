# Example systemd (no secrets in git)

Put tokens and document URLs in `/etc/openflux/*.env` (`chmod 600`), not in unit files.

Shared RST drop on the **exit** host (once):

```ini
# /etc/systemd/system/openflux-rst.service
[Unit]
Description=OpenFlux shared iptables RST drop
After=network-online.target
[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/bash -c '/usr/sbin/iptables -C OUTPUT -p tcp --tcp-flags RST RST -j DROP 2>/dev/null || /usr/sbin/iptables -A OUTPUT -p tcp --tcp-flags RST RST -j DROP'
[Install]
WantedBy=multi-user.target
```

Exit MAX (bind one public IP):

```
openflux --exit-node --transport oneme --maxToken "$MAX_TOKEN" --bind-ip 203.0.113.10
# DataChannel A/B (must match the client): add --max-payload dc
```

Exit Yandex (bind the other public IP):

```
openflux --exit-node --transport yandex --url "$OPENFLUX_URL" --bind-ip 203.0.113.11
```

Second Yandex pair on the **same** doc (different `--channel` + the other bind IP):

```
openflux --exit-node --transport yandex --url "$OPENFLUX_URL" --bind-ip 203.0.113.10 --channel b
openflux --client --transport yandex --url "$OPENFLUX_URL" --socks5 127.0.0.1:1081 --channel b
```

Client MAX (ICE injection, default):

```
openflux --client --transport oneme --maxToken "$MAX_TOKEN" --maxUid "$MAX_CALLEE_UID" --socks5 127.0.0.1:1081 --call-delay 5
```

Client MAX (DataChannel A/B — same flags on **both** client and exit):

```
openflux --client --transport oneme --max-payload dc --maxToken "$MAX_TOKEN" --maxUid "$MAX_CALLEE_UID,$MAX_CALLEE_UID_2" --socks5 127.0.0.1:1081 --call-delay 5
```

Client Yandex:

```
openflux --client --transport yandex --url "$OPENFLUX_URL" --socks5 127.0.0.1:1080
```

Cups.online (Centrifugo rooms; exit prints a base64 list for `--url` on the client):

```
openflux --exit-node --transport cupsonline --mode proxy --bind-ip 203.0.113.10
openflux --client --transport cupsonline --url "$CUPS_ROOMS_B64" --socks5 127.0.0.1:1084
```

Restarting the exit node mints a new room list; the client URL must be updated to match.

Build: `go build -o openflux .` — do not commit the binary. Do not commit the upstream `universal-bypass-tool` blob.
