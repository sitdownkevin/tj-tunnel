# Contributing

Thanks for taking a look! This is a small project, so a few ground rules keep
it manageable.

## Before you start

- For anything larger than a bug fix, open an issue first so we can agree on
  the approach.
- Never commit real credentials. `docker-compose.yml` ships with
  placeholders only; keep your edited copy out of git (it is listed in
  `.gitignore` as `.env`, `*.local.json`, and a `compose.local.yml` override).
- Never commit captured traffic or packet dumps containing personal data.

## Development

Requirements: Go (see `go.mod`) and Docker if you touch the image.

```sh
make build      # binary in bin/
make test       # unit tests
make test-race  # unit tests with the race detector
make lint       # gofmt check + go vet
```

Dependencies are vendored. **Always build with `-mod=vendor`**; see the note in
the README about the gVisor test-file package clash.

## Code layout

```
cmd/tjsocks/       entry point: flags, reconnect loop, wiring
internal/config/   configuration precedence and validation
internal/protocol/ gateway login, tunnel framing, control packets
internal/tunnel/   gVisor netstack bridge and keepalives
internal/resolver/ DNS over the tunnel, with caching
internal/netstack/ TCP/UDP dialling inside the tunnel
internal/socks/    SOCKS5 server: auth, CONNECT, UDP ASSOCIATE
```

Guidelines:

- Keep packages focused; new behaviour goes in the package that owns it.
- Logging uses `log/slog`; errors are returned, not logged, unless they are
  recoverable inside a request handler.
- Any new guest-visible behaviour needs a test. `internal/socks` and
  `internal/config` show how to test without a real VPN connection.
- Prefer small interfaces (see `socks.Resolver` / `socks.Dialer`) over concrete
  dependencies so tests stay fast and hermetic.

## Pull requests

- Keep commits focused and use conventional prefixes (`feat:`, `fix:`,
  `docs:`, `chore:`).
- CI must be green: formatting, vet, tests (with `-race`), and the image build.
- Describe the change and how you verified it (which client, TCP/UDP, etc.).

## Licence

By contributing you agree your work is released under the [MIT licence](LICENSE).
