# Changelog

All notable changes to this project are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.2.0] - 2026-10-27

### Changed

- **Renamed the project to TJ Tunnel**: module `github.com/sitdownkevin/tj-tunnel`,
  binary `tj-tunnel`, image `ghcr.io/sitdownkevin/tj-tunnel`, container
  `tj-tunnel`. All branding references to the upstream university were removed;
  the product is only referred to as "TJ Tunnel".
- **The dashboard is now a React + MUI single-page app**, built by Vite and
  embedded into the binary. Replaces the hand-written HTML page.
- **Status is pushed over WebSocket** instead of SSE, with exponential backoff
  reconnect and a fallback to the login screen when the session expires.
- **Login happens in a page form** with a real session: `POST /api/login` sets
  an HMAC-signed, HttpOnly cookie (12 h). Browser basic-auth popups are gone.
- The dashboard shows ready-to-copy SOCKS5 connection details (link, host, port,
  UDP support, auth mode, proxy username) with per-field copy buttons.
- `WEB_USER` / `WEB_PASS` now control the dashboard login, falling back to the
  SOCKS credentials when unset.

### Added

- Live logs in the dashboard: a bounded in-memory ring (500 entries) fed by a
  `slog` handler that mirrors everything written to stderr, streamed over the
  same WebSocket and also available via `GET /api/logs`. The panel supports
  level filtering, auto-scroll, pause and clear.
- Tunnel control from the dashboard: start/stop buttons and an auto-reconnect
  switch (`PUT /api/tunnel`, `GET /api/tunnel`).
- `AUTOSTART` setting. It defaults to `false`, so a fresh container waits for a
  "connect" click in the dashboard; without a dashboard it is forced on.

### Fixed

- Static assets used a fixed filename with `immutable` caching, so browsers kept
  serving stale JavaScript after an upgrade. Bundle files are content-hashed now.

## [0.1.3] - 2026-10-27

### Added

- Built-in web dashboard (`WEB_LISTEN`) showing tunnel phase, tunnel IP,
  gateway, DNS, live up/down rates, totals, connection counts and the last
  error. The page is a single embedded HTML file with no framework, updated
  over Server-Sent Events once per second, with automatic reconnect and a
  polling fallback.
- Dashboard authentication reuses the SOCKS credentials by default, and
  `WEB_USER` / `WEB_PASS` override them.

### Fixed

- Session teardown could deadlock: the UDP relay waited for its reader before
  the socket was closed, so a dropped tunnel never logged the disconnect and
  never reconnected. Teardown now cancels, closes, then waits, and the
  dashboard reflects "reconnecting" immediately.
- `auth_method` defaulted to an empty string instead of `LDAP3` in reports.

## [0.1.2] - 2026-10-27

### Added

- `-diagnose`: walks DNS → TCP → TLS → login against the gateway and reports
  exactly which step fails. Run it in the container with
  `docker compose run --rm tj-tunnel -diagnose`.

### Fixed

- SOCKS5 requests used to hang while the VPN tunnel was down (the listener
  accepted connections but nothing served them). The proxy now serves for the
  whole process and answers immediately with a failure reply, so clients fail
  fast instead of timing out, and the server logs why.
- The UDP relay socket is now fully released between reconnects, so a quick
  reconnect can no longer fail to bind its port.

## [0.1.1] - 2026-10-27

### Changed

- Container images are published only to the GitHub Container Registry
  (`ghcr.io/sitdownkevin/tj-tunnel`); Docker Hub support and its secrets were
  removed, so publishing needs no configuration at all.
- All settings now live inline in `docker-compose.yml`; the `.env` file and
  `.env.example` are gone, so deployment is a single file.

## [0.1.0] - 2026-10-27

### Added

- SOCKS5 server with `CONNECT`, `UDP ASSOCIATE` and optional RFC 1929
  username/password authentication.
- campus VPN (Sangfor SSL-VPN) login and IP-over-TLS tunnel client.
- User-space TCP/IP stack via gVisor netstack: no root, no TUN device, no host
  route changes.
- DNS resolution through the gateway's DNS servers, with TTL caching and retry.
- Automatic reconnection with capped exponential backoff.
- Multi-stage container image on distroless, non-root, ~10 MB.
- Docker Compose deployment file, `-healthcheck` probe and GitHub Actions
  workflows for CI and multi-arch image publishing.
