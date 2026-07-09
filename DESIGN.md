# owlwatch — design document (v1)

Single-container host monitoring with a beautiful web UI. One Go binary serves
an embedded React dashboard showing **CPU, GPU, RAM and disk** in realtime
(SSE, 2s cadence) and over time (SQLite history, 1h → 30d ranges).

This document is the authoritative design reference for contributors: it
records the architecture, the package contracts, the wire formats and the UI
spec the implementation follows. The shared data types live in
`internal/metrics/types.go` and `web/src/lib/types.ts` — those two files
mirror each other exactly; change them only together, and treat a change there
as a change to the wire format.

## 1. Architecture

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

- **Realtime path:** collector samples every 2s into a ring buffer (last 5 min)
  and broadcasts to subscribers. The SSE endpoint streams every tick.
- **History path:** a persistence pump (in `main.go`) subscribes to the
  collector and inserts one row every 10s. Queries aggregate into buckets
  server-side (≤ ~400 points per response). Retention 30 days, pruned hourly.
- **No auth in v1** — intended for LAN / behind a reverse proxy. The README
  calls this out prominently.

Repository layout:

| Path | Purpose |
|---|---|
| `internal/metrics/` | shared data types (Snapshot, HostInfo, HistoryPoint) — mirrored by `web/src/lib/types.ts` |
| `internal/collector/` | metric sampling: gopsutil + `nvidia-smi`, ring buffer, subscriber broadcast |
| `internal/store/` | SQLite persistence: schema, bucketed history queries, pruning |
| `internal/server/` | HTTP server: JSON API, SSE stream, embedded UI, middleware |
| `cmd/owlwatch/` | env config parsing, wiring, persistence pump, `-healthcheck` probe |
| `web/` | React dashboard (Vite + TypeScript); `web/embed.go` embeds the built `web/dist` |
| `Dockerfile`, `docker-compose.yml` | container build and the canonical deployment |

## 2. Configuration (environment variables)

| Var | Default | Meaning |
|---|---|---|
| `OWLWATCH_PORT` | `8080` | HTTP listen port |
| `OWLWATCH_DB` | `./data/owlwatch.db` | SQLite path (Docker sets `/data/owlwatch.db`) |
| `OWLWATCH_SAMPLE_INTERVAL` | `2s` | live sampling cadence (Go duration) |
| `OWLWATCH_PERSIST_INTERVAL` | `10s` | history write cadence |
| `OWLWATCH_RETENTION_DAYS` | `30` | history retention |
| `OWLWATCH_ROOTFS` | *(empty)* | container mode: host `/` bind-mounted here (e.g. `/host/rootfs`) |
| `OWLWATCH_ALLOWED_HOSTS` | *(empty)* | extra Host-header names to accept (comma-separated). IP-literal hosts and `localhost` are always accepted; other names are rejected with 421 to block DNS rebinding |
| `HOST_PROC`, `HOST_SYS`, `HOST_ETC`, `HOST_VAR`, `HOST_RUN` | *(unset)* | standard gopsutil redirects; set by docker-compose to `/host/proc` etc. |

Config parsing lives in `cmd/owlwatch/main.go` (plain `os.Getenv` + defaults,
no config library). The binary also supports `owlwatch -healthcheck` which GETs
`http://127.0.0.1:$OWLWATCH_PORT/healthz` and exits 0/1 — used by Docker
`HEALTHCHECK` (final image has no shell/curl).

## 3. Package APIs (public contract — keep these signatures stable)

### 3.1 `internal/collector`

```go
type Config struct {
    SampleInterval time.Duration // default 2s
    Rootfs         string        // OWLWATCH_ROOTFS; "" = native mode
    RingSize       int           // default 150 (5 min at 2s)
}

func New(cfg Config) *Collector

// Run blocks, sampling on a ticker until ctx is cancelled.
func (c *Collector) Run(ctx context.Context)

// Subscribe returns a channel of future snapshots plus a cancel func.
// Slow subscribers must never block the sampler (drop, don't block).
func (c *Collector) Subscribe() (<-chan metrics.Snapshot, func())

func (c *Collector) Latest() (metrics.Snapshot, bool) // false before first sample
func (c *Collector) Recent() []metrics.Snapshot      // ring buffer copy, oldest first
func (c *Collector) HostInfo() metrics.HostInfo      // cached at startup
```

Implementation notes:

- Use `github.com/shirou/gopsutil/v4` (`cpu`, `mem`, `disk`, `load`, `host`
  subpackages) with the `WithContext` variants. gopsutil natively honors
  `HOST_PROC`/`HOST_SYS` env vars — no extra work for CPU/mem/load in Docker.
- **CPU:** `cpu.Percent(0, false)` for combined and `cpu.Percent(0, true)` for
  per-core (delta since previous call — call once at startup to prime, and
  never pass a non-zero interval, which would sleep). Load via
  `load.Avg()` (returns zeros on platforms without it — fine).
- **Disk:** enumerate `disk.Partitions(false)`. Keep only real filesystems
  (allowlist: ext4, ext3, ext2, xfs, btrfs, zfs, apfs, hfs, hfsplus, ntfs,
  fuseblk, vfat, exfat, f2fs). Skip mounts under `/boot/efi`, `/System`,
  `/private/var/vm`, and anything whose device doesn't start with `/dev/`
  (also allow `zfs` datasets whose device has no `/dev/` prefix). Dedupe by
  device, keeping the shortest mountpoint. Sort by mountpoint.
  - **Container mode** (`Rootfs != ""`): partitions are read from the *host's*
    mount table automatically via `HOST_PROC`. For each kept partition, call
    `disk.Usage(filepath.Join(cfg.Rootfs, p.Mountpoint))` — the host's `/` is
    recursively bind-mounted at Rootfs, so statfs through it reports host
    filesystems. Report the *host* mountpoint in `DiskMetrics.Mount`. If
    `disk.Usage` errors for a mount (not bind-visible), skip that mount.
  - **Native mode:** `disk.Usage(p.Mountpoint)` directly.
- **GPU (`gpu.go`):** poll `nvidia-smi
  --query-gpu=index,name,utilization.gpu,memory.total,memory.used,temperature.gpu,power.draw
  --format=csv,noheader,nounits` with a 3s `exec.CommandContext` timeout.
  Parse defensively: fields may be `[N/A]` or `[Not Supported]` → 0. Memory
  values are MiB → convert to bytes. If the binary is missing (LookPath fails)
  or the first probe fails, mark no-GPU and **don't re-exec every tick** —
  re-probe at most once a minute so a driver appearing later is picked up, but
  a GPU-less host isn't forking a process 30×/min. `HostInfo.HasGPU` /
  `GPUNames` come from the first successful probe (or false/empty).
- **HostInfo:** `host.Info()` (hostname, platform, kernel, boot time),
  `cpu.Info()` for model name, `runtime.GOARCH`, `runtime.NumCPU()`.
  `HostInfo()` fills everything except `Version`; `main.go` stamps `Version`
  (from `-ldflags`) on the returned struct before handing it to the server.
- Sampling must be resilient: any single failing metric logs once (rate-limited)
  and zero-values that section, never crashes, never skips the tick.
- Unit tests cover the pure logic (partition filtering, nvidia-smi CSV
  parsing) — table-driven, no mocking frameworks.

### 3.2 `internal/store`

```go
func Open(path string) (*Store, error) // mkdir -p parent, open, migrate schema

func (s *Store) Insert(snap metrics.Snapshot) error

type Range struct {
    Key    string        // "1h"
    Dur    time.Duration // how far back
    Bucket time.Duration // aggregation bucket
}

// Ranges maps the five supported keys:
// 1h/10s, 6h/1m, 24h/5m, 7d/30m, 30d/2h.
var Ranges = map[string]Range{ ... }

func (s *Store) Query(r Range, now time.Time) ([]metrics.HistoryPoint, error)
func (s *Store) Prune(olderThan time.Duration) error
func (s *Store) Close() error
```

- Driver: `modernc.org/sqlite` (pure Go, registers as driver name `sqlite`).
  Opened with the DSN
  `<path>?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)` —
  modernc runs each `_pragma` query parameter as a PRAGMA on every new
  connection. The DSN syntax is modernc-specific (a known footgun) and is
  guarded by a test (`TestPragmasApplied` in `store_test.go`).
- Schema (migrated on open, `CREATE TABLE IF NOT EXISTS`):

```sql
CREATE TABLE IF NOT EXISTS samples (
  ts        INTEGER PRIMARY KEY,  -- unix ms
  cpu_pct   REAL NOT NULL,
  mem_used  INTEGER NOT NULL,
  mem_pct   REAL NOT NULL,
  swap_used INTEGER NOT NULL,
  gpu_util  REAL,     -- NULL when no GPU: avg across GPUs
  gpu_mem   INTEGER,  -- sum across GPUs
  gpu_temp  REAL      -- max across GPUs
);
CREATE TABLE IF NOT EXISTS disk_samples (
  ts       INTEGER NOT NULL,
  mount    TEXT NOT NULL,
  used_pct REAL NOT NULL,
  PRIMARY KEY (ts, mount)
) WITHOUT ROWID;
```

- `Insert` flattens the Snapshot (GPU aggregation as commented above) in one
  transaction.
- `Query` buckets with integer math on ms timestamps:
  `GROUP BY ts / bucketMs`, selecting `avg(cpu_pct), max(cpu_pct), avg(mem_used),
  avg(mem_pct), avg(swap_used), avg(gpu_util), max(gpu_util), avg(gpu_mem),
  max(gpu_temp)`; bucket TS = `(ts / bucketMs) * bucketMs`. Disk usage queried
  separately with the same bucketing (`avg(used_pct)` per mount) and merged
  into the HistoryPoints in Go. GPU pointer fields are nil when the aggregate
  is NULL. `Disks` map is always non-nil.
- `Prune` deletes from both tables; called hourly by main.go.
- Unit tests run against a temp-file DB: insert synthetic snapshots across a
  time span, assert bucket counts/averages, prune behavior, and GPU-null
  handling.

### 3.3 `internal/server` + `cmd/owlwatch`

```go
type Config struct {
    Addr           string // ":8080"
    Collector      *collector.Collector
    Store          *store.Store
    Host           metrics.HostInfo // Version already filled in
    SampleInterval time.Duration    // collector cadence: hello intervalMs and the healthz staleness bound
    AllowedHosts   []string         // OWLWATCH_ALLOWED_HOSTS: extra Host-header names accepted
}

func New(cfg Config) *Server
func (s *Server) ListenAndServe(ctx context.Context) error // graceful shutdown on ctx cancel
```

Routes (stdlib `http.ServeMux`, Go 1.22 patterns; no router dependency):

- `GET /api/host` → `metrics.HostInfo` JSON.
- `GET /api/live` → SSE stream:
  - Headers: `Content-Type: text/event-stream`, `Cache-Control: no-cache`,
    `X-Accel-Buffering: no`.
  - On connect, send one `hello` event: `{"host": HostInfo, "recent": [Snapshot…],
    "intervalMs": <sample interval in ms>}` (ring buffer, oldest first), then a
    `snapshot` event per collector tick.
    Wire format per event: `event: hello\ndata: <one-line JSON>\n\n`.
  - Comment heartbeat (`: ping\n\n`) every 15s so proxies don't idle-close.
  - Client disconnect (request context done) must unsubscribe. Slow clients
    are dropped by the collector's non-blocking send — on channel close, end
    the response.
- `GET /api/history?range=1h|6h|24h|7d|30d` → `{"range":"1h","points":[…]}`.
  Unknown range → 400 JSON `{"error":"..."}`. Points may be empty, never null.
- `GET /healthz` → `200 ok` when the collector has produced a sample recently
  (within 5× the sample interval); `503` before the first sample or when the
  latest sample is stale (a wedged sampler must flip health so Docker restarts
  the container).
- Everything else: serve the embedded UI from `web.Dist` (`fs.Sub` to `dist`),
  with an SPA fallback (unknown non-`/api` path → `index.html`). Set
  `Cache-Control: no-cache` on `index.html`, long cache on hashed assets
  (`/assets/`).
- Middleware: request logging (one line: method, path, status, duration),
  panic recovery, and a Host-header check (DNS-rebinding guard): IP-literal
  hosts and `localhost` are always accepted, names listed in `AllowedHosts`
  are accepted, everything else gets 421. `Server.ListenAndServe` uses
  `http.Server` with sane timeouts — **but no WriteTimeout on the SSE route**
  (set `WriteTimeout: 0` globally and rely on heartbeats + client contexts;
  the code comments explain why).

`cmd/owlwatch/main.go` wires everything:

1. Parse env config (§2); `-healthcheck` flag short-circuits as described.
2. `store.Open`, `collector.New`, start `collector.Run` in a goroutine.
3. Persistence pump goroutine: subscribe to collector, `time.Ticker` at
   `OWLWATCH_PERSIST_INTERVAL`, insert the latest snapshot each tick; hourly
   `Prune`.
4. `server.New(...).ListenAndServe(ctx)`.
5. `signal.NotifyContext` (SIGINT/SIGTERM) → graceful shutdown, close store.
6. Version: `var version = "dev"` package var (set via `-ldflags "-X main.version=…"`).
7. Startup log: one friendly line with port, DB path, GPU yes/no.

### 3.4 Frontend — see §5 for the full UI spec

### 3.5 Docker & docs — see §6

## 4. HTTP/SSE contract summary

Everything the UI consumes, in one place: `GET /api/host` (HostInfo),
`GET /api/live` (SSE: one `hello`, then `snapshot` every 2s),
`GET /api/history?range=K` (HistoryResponse). Types exactly as in
`web/src/lib/types.ts`.

## 5. Frontend spec

### 5.1 Stack

- Vite + React 18 + TypeScript, **no UI/chart libraries** — charts are
  hand-rolled SVG (spec below), styling is plain CSS with custom-property
  design tokens. Dependencies: `react`, `react-dom` and dev-deps
  (`vite`, `@vitejs/plugin-react`, `typescript`, `@types/react`,
  `@types/react-dom`). Nothing else.
- `vite.config.ts`: `server.proxy` `/api` → `http://localhost:8080`
  (SSE needs no special flag beyond default; set `changeOrigin: false`).
  Build output `web/dist` (default).
- `npm run build` must pass (`tsc && vite build`) — it is the frontend
  build gate.

### 5.2 Design tokens (in `src/styles/tokens.css`, exactly these values)

Theme mechanism: `<html data-theme="dark|light">`, defaulting to the OS
preference on first load, persisted to localStorage on toggle. A
`?theme=dark|light` URL parameter overrides both for that page load without
persisting (deep links, screenshots). Dark is the canonical/default look.

```css
:root[data-theme="light"] {
  --page:      #f9f9f7;  --surface:  #fcfcfb;
  --ink:       #0b0b0b;  --ink-2:    #52514e;  --ink-muted: #898781;
  --grid:      #e1e0d9;  --baseline: #c3c2b7;
  --border:    rgba(11,11,11,0.10);
  --series-1:  #2a78d6;  /* blue    — CPU        */
  --series-2:  #1baf7a;  /* aqua    — Memory     */
  --series-3:  #eda100;  /* yellow  — disk slot 3 */
  --series-4:  #008300;  /* green   — disk slot 4 */
  --series-5:  #4a3aa7;  /* violet  — GPU        */
  --series-6:  #e34948;  /* red     — disk slot 6 */
  --series-7:  #e87ba4;  /* magenta — disk slot 7 */
  --series-8:  #eb6834;  /* orange  — disk slot 8 */
  --status-good: #0ca30c; --status-warn: #fab219;
  --status-serious: #ec835a; --status-critical: #d03b3b;
  --delta-good: #006300;
}
:root[data-theme="dark"] {
  --page:      #0d0d0d;  --surface:  #1a1a19;
  --ink:       #ffffff;  --ink-2:    #c3c2b7;  --ink-muted: #898781;
  --grid:      #2c2c2a;  --baseline: #383835;
  --border:    rgba(255,255,255,0.10);
  --series-1:  #3987e5;  --series-2:  #199e70;  --series-3:  #c98500;
  --series-4:  #008300;  --series-5:  #9085e9;  --series-6:  #e66767;
  --series-7:  #d55181;  --series-8:  #d95926;
  --status-good: #0ca30c; --status-warn: #fab219;
  --status-serious: #ec835a; --status-critical: #d03b3b;
  --delta-good: #0ca30c;
}
```

Font: `system-ui, -apple-system, "Segoe UI", sans-serif` everywhere — no
webfonts, no display faces. Large numbers use default proportional figures;
`font-variant-numeric: tabular-nums` ONLY on axis ticks and table columns.

Fixed series identity (color follows the entity, never rank): CPU = series-1,
Memory = series-2, GPU = series-5. Disk mounts take slots 1..8 first-seen from
the shared sticky assigner in `web/src/lib/diskSlots.ts` (used by both the disk
chart and the disk tile) so a mount keeps its hue across range switches.

### 5.3 Layout

```
┌ header ─ 🦉 owlwatch · hostname · platform chip · uptime · ● Live · ◐ theme ┐
├ stat tiles (grid auto-fit, min 220px) ─────────────────────────────────────┤
│  CPU          Memory          GPU (if present)     Disk                    │
├ history ────────────────────────────────────────────────────────────────── ┤
│  [1h] [6h] [24h] [7d] [30d]          ← one row, left-aligned, above charts │
│  ┌ CPU usage ────────────┐  ┌ Memory ───────────────┐                      │
│  ┌ GPU (if present) ─────┐  ┌ Disk usage ───────────┐                      │
└─────────────────────────────────────────────────────────────────────────── ┘
```

- Page background `--page`; cards are `--surface` with `--border` hairline ring
  and 12px radius. Max content width ~1200px, centered. Responsive: tiles and
  charts stack to one column under 720px. Charts resize via ResizeObserver.
- Header: owl emoji or tiny inline SVG mark, hostname in semibold, platform +
  arch as a muted chip, uptime ticking live (computed from `bootTime`),
  connection status, theme toggle button.
- **Connection status is a status, not decoration:** green dot + "Live" when
  SSE is open; amber dot + "Reconnecting…" when the EventSource errors (it
  auto-reconnects). Icon + label, never color alone.

### 5.4 Stat tiles (dataviz stat-tile contract)

Each tile: `label` (sentence case, muted) · `value` (28–32px semibold,
proportional figures) · `sublabel` (secondary ink) · 60-point sparkline (last
5 min from the ring buffer + live ticks) · thin meter bar where a capacity
exists.

- **CPU** — value: `37.4%`; sublabel: `12 cores · load 1.24`; sparkline of
  usagePct; meter of usagePct.
- **Memory** — value: `12.4 GiB`; sublabel: `of 32 GiB · 39%` (+ swap when
  swapUsed > 0); sparkline of usedPct; meter of usedPct.
- **GPU** (hidden entirely when `hasGPU` is false) — value: `62%`; sublabel:
  `GPU name · 4.1 / 24 GiB · 61°C`; sparkline of utilPct (first GPU, or avg);
  meter of utilPct. Multiple GPUs: tile shows the average and the sublabel
  says `2× <name>`.
- **Disk** — headline is the fullest real mount: value `72%`; sublabel
  `/ · 412 GiB free`; below it a mount list (up to 3 mounts with mini-meters)
  instead of a sparkline. Mount hues come from the shared slot assigner so
  they match the disk chart.
- Meter: 4px track, full-width; fill color = series hue normally, switches to
  `--status-warn` ≥ 80%, `--status-critical` ≥ 92%; the unfilled track is the
  same hue at ~18% opacity. When a meter is in a status state, the tile's
  sublabel gains a small `▲ high` marker — icon + text, never color alone.
- Sparkline: 2px line in the tile's series hue, no axes, no fill, last point
  marked with a 4px dot. It's a bare stat-tile trend — no tooltip needed.

### 5.5 Time-series charts (the core component — built once, used everywhere)

One `TimeSeriesChart` React component, hand-rolled SVG:

- **Marks:** 2px lines, round join/cap; area fill of the series hue at 10%
  opacity from line to baseline (single-series charts only; the multi-series
  disk chart is lines-only). Last point of each series gets a 4px-radius dot
  with a 2px `--surface` ring.
- **Grid:** 3–4 horizontal hairlines (1px, `--grid`, solid), no vertical
  gridlines; baseline in `--baseline`. Y-axis: clean round ticks in
  `--ink-muted` 11px `tabular-nums`, left, outside the plot. X-axis: 4–6 time
  ticks, format by range (1h/6h → `14:05`, 24h → `Tue 14:00`, 7d/30d →
  `Jun 12`).
- **Y domain:** percent charts fixed 0–100. Byte charts (memory) 0 → total
  (fixed capacity domain, ticks in GiB). Never auto-zoom a percent axis.
- **Hover layer (required):** a vertical crosshair hairline snaps to the
  nearest bucket; one tooltip shows ALL series at that X: each row =
  short 12×2px line-key in the series color · series name in `--ink-2` ·
  value in `--ink` semibold (values lead). For CPU/GPU include `peak` from
  the max field. Tooltip is a positioned div (surface bg, border, 8px radius,
  shadow), flips side near edges, hidden on pointerleave. Keyboard: the chart
  is focusable; ←/→ move the crosshair bucket-by-bucket; same tooltip on
  focus. All names/values inserted via textContent (React text nodes — never
  dangerouslySetInnerHTML).
- **Legend:** multi-series charts only (disk): one row under the title —
  swatch = 12×2px line-key + mount name in `--ink-2`. Single-series charts
  have no legend (the card title names the metric).
- **Table view:** each chart card's header has a small icon-button toggle
  (chart ⇄ table). Table = scrollable (max-height of the plot), time +
  one column per series, `tabular-nums`, same data. This is the
  accessibility fallback — required.
- **Refetch keeps the frame:** on range change or 60s auto-refresh, keep the
  old render at 0.5 opacity until new data arrives (no skeleton, no layout
  jump). History auto-refreshes every 60s.
- Empty/sparse: < 2 points → centered muted "Collecting data — check back in a
  minute." Card never collapses in height.

Chart cards in v1: **CPU usage %** (avg line + area; tooltip shows avg &
peak) · **Memory** (used bytes, capacity domain; tooltip adds %) ·
**GPU utilization %** (only when hasGPU; tooltip adds temp & VRAM) ·
**Disk usage %** (one line per mount, 0–100, legend, tooltip lists all
mounts). Two-column grid ≥ 960px, one column below.

### 5.6 Range picker

Single row above the chart grid, left-aligned (dataviz filter rules): five
preset buttons `1h 6h 24h 7d 30d`, selected = subtle filled pill with a bold
label, hover = ghost wash. It scopes every history chart below; the live tiles
are always-live and unaffected. Default `1h`. Persist selection in
localStorage.

### 5.7 Data layer (`src/lib/api.ts`)

- `connectLive(handlers)`: wraps `EventSource('/api/live')`; parses `hello`
  and `snapshot` events; exposes connection state (open/reconnecting);
  EventSource reconnects automatically — reflect state via `onerror`/`onopen`.
  Keep a client-side ring buffer (cap ~150) feeding tiles + sparklines.
- `fetchHistory(range)`: plain fetch, typed `HistoryResponse`; abort the
  in-flight request when the range changes (AbortController).
- `fetchHost()`: typed HostInfo (used as fallback if SSE is slow; hello event
  normally supplies it).
- `src/lib/format.ts`: `formatBytes` (binary units, 1 decimal: `12.4 GiB`),
  `formatPct` (1 decimal), `formatUptime` (`12d 4h 07m`), time-tick
  formatters per range. Pure functions, obviously correct by inspection.

### 5.8 Quality bar (from the dataviz method — non-negotiable)

- Text never wears a series color; identity comes from a colored mark beside
  text. One y-axis per chart, ever. No dashed gridlines. No value labels on
  every point — the tooltip and table carry detail. Legend only for ≥2 series.
- The dashboard must look intentional in BOTH themes — check both when
  touching styles.
- `index.html`: title `owlwatch · <set from hostname at runtime>`, owl 🦉
  favicon via inline SVG data URI, `<meta name="color-scheme" content="dark light">`,
  theme-init inline script (reads localStorage before first paint — no flash).

## 6. Docker & deployment

Multi-stage `Dockerfile`:

1. `node:20-alpine` — `COPY web/`, `npm ci`, `npm run build`.
2. `golang:1.24-alpine` — copy module files + source, copy `web/dist` from
   stage 1 into `web/dist`, `CGO_ENABLED=0 go build -trimpath -ldflags "-s -w
   -X main.version=$VERSION" ./cmd/owlwatch`. `ARG VERSION=dev`.
3. Final: `gcr.io/distroless/base-debian12` (glibc present for the injected
   `nvidia-smi`; no shell). Copy binary. `ENV OWLWATCH_DB=/data/owlwatch.db`,
   `EXPOSE 8080`, `VOLUME /data`,
   `HEALTHCHECK --interval=30s --timeout=5s CMD ["/owlwatch","-healthcheck"]`
   (exec form is mandatory — distroless has no shell for a string-form CMD),
   `ENTRYPOINT ["/owlwatch"]`.

`docker-compose.yml` (the canonical way to run):

```yaml
services:
  owlwatch:
    build: .
    container_name: owlwatch
    ports: ["8080:8080"]
    restart: unless-stopped
    environment:
      HOST_PROC: /host/proc
      HOST_SYS: /host/sys
      HOST_ETC: /host/etc
      OWLWATCH_ROOTFS: /host/rootfs
    volumes:
      - /proc:/host/proc:ro
      - /sys:/host/sys:ro
      - /etc:/host/etc:ro
      - /:/host/rootfs:ro,rslave   # rslave so host mounts added later propagate
      - owlwatch-data:/data
    # For NVIDIA GPUs, uncomment (requires nvidia-container-toolkit):
    # gpus: all
volumes:
  owlwatch-data:
```

Prebuilt images are published to `ghcr.io/cleveroab/owlwatch` from `main` by
CI. The `README.md` is the user-facing companion to this document: quick start
(compose + prebuilt image), GPU setup, the no-auth warning, the §2 config
table, local development, API reference and license. Keep the two documents
consistent — the code is the truth, this document explains it, the README
sells and operates it.

`.dockerignore`: `web/node_modules`, `web/dist`, `data`, `.git`, `*.db*`.

## 7. Dev workflow

- Backend: `go run ./cmd/owlwatch` (macOS works natively — no GPU, that's fine).
- Frontend: `cd web && npm run dev` → Vite on 5173 proxying `/api` to 8080.
- Full build: `make build` (frontend first — `web/dist` is gitignored and
  `go:embed` needs it — then the Go binary); `make run` builds and runs it.
- Production check: `cd web && npm run build && cd .. && go build ./... && ./owlwatch`.

## 8. Non-goals for v1 (do not build)

Network I/O metrics, per-process lists, alerts/notifications, auth, multi-host
agents, AMD/Intel/Apple GPU support (NVIDIA only), historical per-core CPU,
config files. The GPU history aggregates across cards (per-GPU history is v2).
