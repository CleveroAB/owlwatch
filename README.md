# 🦉 owlwatch

**Single-container host monitoring with a beautiful web UI.** One static Go
binary serves an embedded React dashboard showing **CPU, GPU, RAM and disk** —
live (updated every 2 seconds over SSE) and over time (SQLite history, 1 hour
to 30 days). No agents, no external database, no config files.

[![CI](https://github.com/CleveroAB/owlwatch/actions/workflows/ci.yml/badge.svg)](https://github.com/CleveroAB/owlwatch/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](https://opensource.org/licenses/MIT)

![owlwatch dashboard (dark theme)](docs/screenshot-dark.png)

*Dark is the default theme; here is the same dashboard in the
[light theme](docs/screenshot-light.png).*

> [!WARNING]
> **owlwatch has no authentication and no TLS.** Anyone who can reach the port
> can see everything the dashboard shows. Run it on a trusted LAN, over a VPN
> such as Tailscale, or behind a reverse proxy that handles auth (nginx or
> Caddy basic auth work fine). Do not expose it directly to the internet.
> It is read-only — metrics out, nothing in — but treat host telemetry as
> sensitive anyway.

## Quick start

### From a clone

```sh
git clone https://github.com/CleveroAB/owlwatch.git
cd owlwatch
docker compose up -d --build
```

Open <http://localhost:8080>. That's it.

To stamp the build with a version, pass
`--build-arg VERSION=$(git describe --tags --always)` to `docker build`
(compose users: add it under `build.args`).

### Prebuilt image

No clone needed — images are published to GitHub Container Registry from
`main` by CI:

```sh
docker run -d --name owlwatch \
  -p 8080:8080 \
  --restart unless-stopped \
  -e HOST_PROC=/host/proc \
  -e HOST_SYS=/host/sys \
  -e HOST_ETC=/host/etc \
  -e OWLWATCH_ROOTFS=/host/rootfs \
  -v /proc:/host/proc:ro \
  -v /sys:/host/sys:ro \
  -v /etc:/host/etc:ro \
  -v /:/host/rootfs:ro,rslave \
  -v owlwatch-data:/data \
  ghcr.io/cleveroab/owlwatch:latest
```

### Why all those mounts?

Both commands bind-mount a few host paths read-only so the numbers describe
the **host**, not the container:

| Mount | Why |
|---|---|
| `/proc` → `/host/proc` | host CPU, memory, load, mount table |
| `/sys` → `/host/sys` | host sensors and system info |
| `/etc` → `/host/etc` | host OS identification (`os-release` etc., via gopsutil's `HOST_ETC`) |
| `/` → `/host/rootfs` | disk usage measured against host filesystems; host hostname (read from `/host/rootfs/etc/hostname`, since the container only sees its own UTS hostname) |
| `owlwatch-data` volume → `/data` | SQLite history (survives restarts; delete the volume to reset) |

The rootfs mount uses `ro,rslave` (the same pattern node_exporter documents):
with the default private propagation, filesystems mounted on the host *after*
the container starts would not appear inside it, and their disk usage would
silently be read from the empty mountpoint directory underneath. The container
runs as root — that's required to read the host's `/proc` and `/sys` through
the bind mounts.

Host monitoring from a container works on **Linux hosts**. On Docker Desktop
(macOS/Windows) you'll be watching Docker's Linux VM, not your machine — run
the binary natively instead (see [Local development](#local-development)).

## GPU support (NVIDIA only in v1)

owlwatch reads GPU utilization, VRAM, temperature and power by polling
`nvidia-smi`. Inside the container that binary comes from the
[NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html),
which injects it — along with the driver libraries — at container start.

1. Install the NVIDIA driver and `nvidia-container-toolkit` on the host.
2. Uncomment `gpus: all` in `docker-compose.yml` (or add `--gpus all` to
   `docker run`).

No GPU, no problem: the GPU tile and chart simply don't render, and nothing
is polled. AMD, Intel and Apple GPUs are not supported in v1.

## Configuration

Everything is environment variables; the defaults are sensible.

| Variable | Default | Meaning |
|---|---|---|
| `OWLWATCH_PORT` | `8080` | HTTP listen port |
| `OWLWATCH_DB` | `./data/owlwatch.db` | SQLite path (the Docker image sets `/data/owlwatch.db`) |
| `OWLWATCH_SAMPLE_INTERVAL` | `2s` | live sampling cadence (Go duration syntax) |
| `OWLWATCH_PERSIST_INTERVAL` | `10s` | how often a sample is written to history |
| `OWLWATCH_RETENTION_DAYS` | `30` | history retention (pruned hourly) |
| `OWLWATCH_ROOTFS` | *(empty)* | container mode: path where the host `/` is bind-mounted (e.g. `/host/rootfs`); empty = native mode |
| `OWLWATCH_ALLOWED_HOSTS` | *(empty)* | extra Host-header names to accept (comma-separated). IP-literal hosts and `localhost` are always accepted; other names are rejected with 421 to block DNS rebinding |
| `HOST_PROC`, `HOST_SYS`, `HOST_ETC`, `HOST_VAR`, `HOST_RUN` | *(unset)* | standard [gopsutil](https://github.com/shirou/gopsutil) redirects; docker-compose sets the first three |

### URL parameters

The dashboard theme normally follows the toggle in the header (persisted to
localStorage) or, before the first toggle, the OS preference. A
`?theme=dark|light` query parameter overrides both for that page load without
persisting anything — handy for deep links, kiosk displays and screenshots:

```
http://localhost:8080/?theme=light
```

## Local development

Requirements: Go 1.24+ and Node 20+.

The Go binary embeds the compiled frontend via `go:embed`, and `web/dist` is
gitignored — so **the frontend must be built before any Go build** or the
embed directive fails. The Makefile encodes that order:

```sh
make build   # npm ci + vite build, then go build → ./owlwatch
make run     # make build, then run it on :8080
```

For UI iteration, run the two dev servers side by side:

```sh
# Terminal 1 — backend on :8080
go run ./cmd/owlwatch

# Terminal 2 — frontend dev server on :5173, /api proxied to :8080
cd web && npm run dev
```

Iterate on the UI at <http://localhost:5173> with hot reload; the Vite proxy
forwards `/api` (including the SSE stream) to the Go backend. Running natively
on macOS works — there's just no GPU section.

## API

Four endpoints, all JSON. The exact shapes live in
[`internal/metrics/types.go`](internal/metrics/types.go) and its mirror
[`web/src/lib/types.ts`](web/src/lib/types.ts).

| Endpoint | Returns |
|---|---|
| `GET /api/host` | static host identity: hostname, platform, kernel, CPU model, cores, total memory, boot time, GPU names, owlwatch version |
| `GET /api/live` | SSE stream — one `hello` event on connect (`{host, recent, intervalMs}` with the last ~5 min of samples and the sample interval), then a `snapshot` event every 2 s; comment heartbeat every 15 s |
| `GET /api/history?range=1h\|6h\|24h\|7d\|30d` | `{range, points}` — server-side bucketed aggregates (≤ ~400 points per response); unknown range → `400 {"error": "..."}` |
| `GET /healthz` | `200 ok` while the latest sample is fresh (within 5× the sample interval), `503` before the first sample or when sampling has stalled; drives the Docker `HEALTHCHECK` |

## Architecture

```
┌────────────────────────── docker container ──────────────────────────┐
│  owlwatch (single static Go binary)                                  │
│                                                                      │
│  collector ──2s ticks──▶ broadcast ──▶ SSE hub ──▶ GET /api/live     │
│   (gopsutil,             │                                           │
│    nvidia-smi)           └──every 10s──▶ store (SQLite @ /data)      │
│                                             │                        │
│  embedded web/dist (go:embed) ◀── React UI  └──▶ GET /api/history    │
└──────────────────────────────────────────────────────────────────────┘
```

- **Live path** — the collector samples every 2 s into a ring buffer (last
  5 min) and broadcasts each snapshot to SSE subscribers. Slow clients are
  dropped rather than allowed to block the sampler.
- **History path** — every 10 s one sample is written to SQLite
  (pure-Go driver, WAL mode). Queries aggregate into time buckets server-side;
  data older than the retention window is pruned hourly.
- **One artifact** — the React UI is compiled to static files and embedded in
  the Go binary; the final image is distroless (glibc but no shell), roughly
  the size of the binary itself.

The full design — package contracts, wire formats, schema, UI spec — lives in
[DESIGN.md](DESIGN.md).

## License

MIT © [Clevero AB](https://clevero.se).
