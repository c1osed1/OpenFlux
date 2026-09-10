# Fork notes (openflux-effusion)

Based on [p1neappleXpress/OpenFlux](https://github.com/p1neappleXpress/OpenFlux). Original remains educational-use. This tree is a practical fork with operational fixes; it is not upstream.

## Code changes vs upstream

- `--bind-ip` — pin exit-node source IPv4 on multi-IP hosts so two transports can share one VPS (different public addresses). See `tunnel.SetBindIP`.
- MAX receiver no longer `os.Exit(1)` when signaling dies; it waits for the next incoming call.
- `--call-delay` (default 3s) — wait before the first MAX outgoing call so the exit listener is up.
- `--max-payload ice|dc` — `ice` (default) keeps signaling ICE-candidate injection; `dc` uses a real WebRTC DataChannel (SDP is actually sent; local description is set).
- MAX caller reconnects with exponential backoff (1s…15s) and can rotate comma-separated `--maxUid` callees.
- Login no longer prints MAX contacts or phone numbers. `--debug` logs sizes, not payloads or address books.
- Periodic `[MAX] stats` (bytes / packets / ready).
- Yandex Docs fails fast if the editor is not legacy OnlyOffice (no type-assert panic on Volga/WOPI).
- `--channel` — Yandex cursor multiplex so two client/exit pairs can share one OnlyOffice doc without mixing packets.

## Extra tooling (this repo)

- `contrib/happ-user` — add/list/delete VLESS users. Override with `HAPP_HOST`, `HAPP_WS_PATH`, `MIHOMO_CFG`.
- `deploy/` — example systemd units using `EnvironmentFile` (no secrets in git).
