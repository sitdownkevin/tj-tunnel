# Security policy

## Reporting a vulnerability

Please **do not** open a public issue for security problems.
Use GitHub's [private vulnerability reporting][advisory] or email the
maintainer listed on the repository profile.

We aim to acknowledge reports within a few days.

[advisory]: https://docs.github.com/en/code-security/security-advisories/guidance-on-reporting-and-writing-information-about-vulnerabilities/privately-reporting-a-security-vulnerability

## Deployment guidance

`TJ Tunnel` is a proxy to a campus network. Treat it like any other network
service exposed to the internet.

1. **Always enable proxy authentication.** Set `SOCKS_USER` and `SOCKS_PASS`.
   Without them anyone who can reach the port can use your campus account.
2. **Prefer a firewall allow-list** over exposing the port to the whole
   internet: restrict `1080/tcp` and `1080/udp` to the IPs you actually use.
3. **Use a strong, unique proxy password.** It is sent in clear text inside
   SOCKS5 (RFC 1929), so it must not be a password you use elsewhere.
4. **Never commit credentials.** Put them in `.env` (ignored by git) or in
   your orchestrator's secret store. `docker-compose.yml` intentionally
   contains no secrets.
5. **Keep the image updated.** Watch releases or enable Dependabot; rebuild
   regularly so base-image fixes are picked up.
6. **Note the single-session limit.** A campus VPN account holds one session at
   a time; sharing an account means the previous session is disconnected.

## What the program does with your credentials

- The username and password are only sent to the configured gateway during
  login, over TLS.
- They are never written to disk by the program. Log lines print the username
  but never the password (`config.Describe`).
- No telemetry, no update checks, no outbound connections except to the
  configured gateway, the tunnel, and the endpoints SOCKS clients request.

## Supported versions

Only the latest release receives fixes.
