# Changelog

Форк [p1neappleXpress/OpenFlux](https://github.com/p1neappleXpress/OpenFlux). База upstream: `4619053`.

Бинарник `universal-bypass-tool` из git убран, собирается `go build -o openflux .`.

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
- `--channel`: формат курсора `18;<канал>;<payload>`; пустой канал — как в upstream (`18;<payload>`).

### Репозиторий

- `contrib/happ-user` — правки пользователей mihomo. Env: `HAPP_HOST`, `HAPP_WS_PATH`, `MIHOMO_CFG`.
- `deploy/README.md` — примеры systemd, без секретов.
- `.gitignore`: `*.env`, ключи, локальный бинарь `openflux`.
