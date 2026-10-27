# Changelog

All notable changes to this project are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.1.0] - 2026-10-27

### Added

- SOCKS5 server with `CONNECT`, `UDP ASSOCIATE` and optional RFC 1929
  username/password authentication.
- Tongji campus VPN (Sangfor SSL-VPN) login and IP-over-TLS tunnel client.
- User-space TCP/IP stack via gVisor netstack: no root, no TUN device, no host
  route changes.
- DNS resolution through the gateway's DNS servers, with TTL caching and retry.
- Automatic reconnection with capped exponential backoff.
- Multi-stage container image on distroless, non-root, ~10 MB.
- Docker Compose deployment file, `-healthcheck` probe and GitHub Actions
  workflows for CI and multi-arch image publishing.
