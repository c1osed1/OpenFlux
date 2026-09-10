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
```

Exit Yandex (bind the other public IP):

```
openflux --exit-node --transport yandex --url "$OPENFLUX_URL" --bind-ip 203.0.113.11
```

Client MAX:

```
openflux --client --transport oneme --maxToken "$MAX_TOKEN" --maxUid "$MAX_CALLEE_UID" --socks5 127.0.0.1:1081 --call-delay 5
```

Client Yandex:

```
openflux --client --transport yandex --url "$OPENFLUX_URL" --socks5 127.0.0.1:1080
```

Build: `go build -o openflux .` — do not commit the binary. Do not commit the upstream `universal-bypass-tool` blob.
