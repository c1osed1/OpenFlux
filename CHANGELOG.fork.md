# Fork notes (openflux-effusion)

Based on [p1neappleXpress/OpenFlux](https://github.com/p1neappleXpress/OpenFlux). Original remains educational-use. This tree is a practical fork with operational fixes; it is not upstream.

## Code changes vs upstream

- `--bind-ip` — pin exit-node source IPv4 on multi-IP hosts so two transports can share one VPS (different public addresses). See `tunnel.SetBindIP`.
- MAX receiver no longer `os.Exit(1)` when signaling dies; it waits for the next incoming call.
- `--call-delay` (default 3s) — wait before the first MAX outgoing call so the exit listener is up.
- Unused `os` import removed from `transport/oneme/max_call.go` after the receiver change.

## Not changed (still upstream behaviour)

- MAX payload still uses ICE-candidate injection (`useICEInjection = true`), not the WebRTC DataChannel `Send` path.
- Yandex Docs still uses cursor/base64 over the old OnlyOffice editor.
- Exit node still needs root + raw sockets; host-wide TCP RST drop is still required.

## Extra tooling (this repo)

- `contrib/happ-user` — add/list/delete VLESS users in a mihomo `config.yaml` without rewriting unrelated keys.
- `deploy/` — example systemd units using `EnvironmentFile` (no secrets in git).
