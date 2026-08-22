# Changelog

All notable changes to owlwatch are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and releases use [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Reboot button at the bottom of each server dashboard. After confirmation it gracefully restarts that Owlwatch process, including remote peers selected through a hub, and reconnects the live dashboard automatically.
- `docker-compose.yml` can now build the image from the checkout: `docker compose up -d --build` compiles frontend and binary from source instead of requiring a published registry image. Pull-based deployment via `OWLWATCH_VERSION` is unchanged.
- Test-email button in the dashboard header (visible only when email alerting is configured), backed by `GET /api/alerts` and `POST /api/alerts/test` — the API's first mutating route, token-gated like the rest.
- Email alerts over plain SMTP (`internal/alerts`): when a metric stays at or above its threshold for a configured duration, owlwatch emails the configured recipients — no new dependencies, stdlib `net/smtp` with opportunistic STARTTLS. Enabled by setting `OWLWATCH_SMTP_HOST`, `OWLWATCH_SMTP_FROM` and `OWLWATCH_ALERT_TO`; thresholds default to CPU 90%, memory 90%, disk 92% (per mount) and GPU 90°C (per card), sustained for `OWLWATCH_ALERT_FOR=5m`, with at most one email per rule per `OWLWATCH_ALERT_COOLDOWN=30m`. Set a threshold to `0` to disable that rule.
- `OWLWATCH_BIND` selects the host interface docker-compose publishes on. It still defaults to `127.0.0.1`, so reaching the dashboard from another machine is now a documented opt-in rather than a compose-file edit.

### Fixed

- Container startup now repairs root-owned `/data` mounts (including persistent bind mounts created by Coolify) before dropping permanently to the non-root application user.

### Removed

- All GitHub Actions workflows: `ci.yml` (build, gofmt, vet, Go and web test suites), `codeql.yml` (code scanning), and `release.yml` (multi-architecture image publish to ghcr.io). Contributions are no longer checked automatically, and release images are built and pushed manually. The `github-actions` Dependabot ecosystem is dropped with them.

## [1.0.0] - 2026-07-12

### Added

- Live CPU, memory, disk, swap, load, and optional NVIDIA GPU telemetry over Server-Sent Events.
- SQLite-backed history with 1 hour, 6 hour, 24 hour, 7 day, and 30 day ranges.
- Single embedded React dashboard with dark/light themes, responsive layouts, keyboard-readable charts, and table views.
- Multi-server federation with fleet overview, per-server dashboards, peer status, and proxied history.
- Optional bearer-token API protection and DNS-rebinding protection through allowed host names.
- Linux amd64/arm64 container images published to GitHub Container Registry.
- Health checks, graceful shutdown, bounded peer requests, retention pruning, and multi-architecture builds.

### Security

- Added explicit exposure guidance, private vulnerability reporting, and browser security headers.

[Unreleased]: https://github.com/CleveroAB/owlwatch/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/CleveroAB/owlwatch/releases/tag/v1.0.0
