# Contributing to owlwatch

Thanks for helping out. This is a small codebase — read `DESIGN.md` first; it
is the architecture contract the code was built against.

## Prerequisites

- Go 1.24+
- Node 20+

## Build order matters

`web/dist` is **not committed** — `web/embed.go` embeds it with `go:embed`, so
any Go build (including `go vet` and `go test`) fails until the UI has been
built once:

```sh
cd web && npm ci && npm run build && cd ..
go build ./cmd/owlwatch
```

The Makefile handles this ordering for you.

## Make targets

| Target | What it does |
|---|---|
| `make build` | Build the web UI, then the `owlwatch` binary |
| `make web` | Build only the web UI (`npm ci` + `npm run build`) |
| `make test` | Web build (includes `tsc` type-check), `go vet`, `go test ./...` |
| `make run` | Build everything and run `./owlwatch` |
| `make docker` | Build the Docker image as `owlwatch:dev` |
| `make clean` | Remove the binary and `web/dist` |

## Running tests

```sh
make test
```

Or directly (after a web build): `go vet ./... && go test ./...`. The frontend
has no separate test suite; `npm run build` runs `tsc`, which is the
type-level check CI enforces.

For day-to-day development run `go run ./cmd/owlwatch` in one terminal and
`cd web && npm run dev` in another — Vite serves on 5173 and proxies `/api`
to 8080.

## Pull requests

- `gofmt` your Go code (CI fails on unformatted files) and make sure
  `make test` passes.
- If your change alters any HTTP/SSE contract, config variable, or the shared
  data types (`internal/metrics/types.go`, `internal/metrics/federation.go` /
  `web/src/lib/types.ts`), update `DESIGN.md` in the same PR — it must stay
  in sync with the code. The unprefixed `/api/host`, `/api/live` and
  `/api/history` aliases are the peer-facing surface hubs consume — frozen,
  do not change them.
- Keep PRs focused; small and reviewable beats big and clever.
