# Changelog

Форк [p1neappleXpress/OpenFlux](https://github.com/p1neappleXpress/OpenFlux). База upstream: `7835b71` (cups.online).

Бинарник `universal-bypass-tool` из git убран, собирается `go build -o openflux .`.

## 2026-09-20

### Upstream merge (`flx-kernel`, 19 коммитов)

- Новый CLI: `--role=client|exit|bench-send|bench-sink`, `--inbound=tun|socks5`,
  `--codec=batched|legacy`, короткие алиасы (`-r -i -t -m -c -u -s -l -d`),
  старые `--client/--exit-node` оставлены как deprecated.
- Транспорт `mailru` (Mail.ru Docs over WebSocket).
- Кодек `batched`: zstd + коалессинг (`transport/batched.go`, `framing.go`),
  по умолчанию; `legacy` = per-packet LZ4.
- `l3` (raw sockets, SNAT/DNAT) и `l4` (gVisor proxy) как бэкенды exit-ноды;
  `--mode raw` = `l3`, `--mode proxy` = `l4` (алиасы для совместимости с юнитами).
- macOS TUN-клиент (`tun_darwin.go`, `tun_learn.go`, `tun_watch.go`), `bench.go`.
- Yandex: batched+zstd канал, backoff реконнекта, precompiled regex.
- Модуль переименован `universal-bypass-tool` -> `openflux`.
- L3 backend: увеличены raw-socket буферы для high-BDP.

Fork-патчи сохранены в merge: `--bind-ip`, `--channel`, `--max-payload ice|dc`,
`--call-delay`, `--maxUid` failover, MAX DataChannel/reconnect, watchdog.

## 2026-09-13

### Upstream merge

- Транспорт `cupsonline` (Centrifugo / cups.online live-coding rooms).
- `vyandex` (Volga editor), AES-256-GCM `--encryption-key-file`.
- Exit mode `--mode proxy|raw`, `--local-ip` для raw.

Fork-патчи сохранены: `--bind-ip`, `--channel`, MAX `dc`/`call-delay`/failover.

## 2026-09-10

### CLI

- `--bind-ip` — исходящий IPv4 exit-ноды (несколько адресов на одном хосте).
- `--call-delay` — пауза перед первым исходящим MAX-звонком (по умолчанию 3 с).
- `--max-payload ice|dc` — туннель MAX: injection в ICE (по умолчанию) или WebRTC DataChannel.
- `--maxUid` — несколько callee через запятую, ротация при ретрае.
- `--channel` — канал курсора Yandex, чтобы два туннеля не мешали друг другу на одном документе.
- Подписи `--maxToken` / `--maxUid` приведены к смыслу флагов.

### MAX (`transport/oneme`)

- Приёмник при обрыве signaling не делает `os.Exit(1)`, ждёт следующий звонок.
- В режиме `dc` SDP уходит в signaling, вызывается `SetLocalDescription`.
- Caller перезванивает сам (backoff 1–15 с).
- Логин больше не печатает контакты и телефоны.
- Ошибка `login.token` не роняет процесс; пустой signaling URL не диалится.
- Раз в 30 с лог байт/пакетов (`[MAX] stats`).

### Yandex (`transport/yandex`)

- Старт падает с понятной ошибкой, если нет legacy OnlyOffice (Volga/WOPI), без panic.
- Очередь Yandex 4096, keepalive 25 с, reconnect с паузой 2–15 с (без шторма при `close 1005`).
- Watchdog рестартит клиент только если SOCKS-порт не слушает, не по таймауту `ifconfig.me`.

### Репозиторий

- `contrib/happ-user` — правки пользователей mihomo. Env: `HAPP_HOST`, `HAPP_WS_PATH`, `MIHOMO_CFG`.
- `deploy/README.md` — примеры systemd, без секретов.
- `.gitignore`: `*.env`, ключи, локальный бинарь `openflux`.
- GitHub Actions: коммит с `[BUILD]` в сообщении (или ручной workflow) собирает бинарники и публикует Release `build-YYYYMMDD-<sha>`.
