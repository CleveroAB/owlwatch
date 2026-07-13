# Changelog

All notable changes to owlwatch are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and releases use [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
