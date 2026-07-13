// Package store persists metric snapshots to SQLite and serves the bucketed
// aggregates behind /api/history. It uses the pure-Go modernc.org/sqlite
// driver, so the binary stays CGO-free.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/CleveroAB/owlwatch/internal/metrics"
)

// Range describes one supported history window: how far back to look and how
// wide each aggregation bucket is.
type Range struct {
	Key    string        // "1h"
	Dur    time.Duration // how far back
	Bucket time.Duration // aggregation bucket
}

// Ranges maps the five supported keys: 1h/10s, 6h/1m, 24h/5m, 7d/30m, 30d/2h.
var Ranges = map[string]Range{
	"1h":  {Key: "1h", Dur: time.Hour, Bucket: 10 * time.Second},
	"6h":  {Key: "6h", Dur: 6 * time.Hour, Bucket: time.Minute},
	"24h": {Key: "24h", Dur: 24 * time.Hour, Bucket: 5 * time.Minute},
	"7d":  {Key: "7d", Dur: 7 * 24 * time.Hour, Bucket: 30 * time.Minute},
	"30d": {Key: "30d", Dur: 30 * 24 * time.Hour, Bucket: 2 * time.Hour},
}

const schema = `
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
`

// Store is a SQLite-backed history store. Safe for concurrent use.
type Store struct {
	db *sql.DB
}

// Open creates the parent directory if needed, opens (or creates) the SQLite
// database at path and migrates the schema.
func Open(path string) (*Store, error) {
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("store: inspect db directory: %w", err)
		}
		// Apply private permissions only to directories we create. An existing
		// parent may be shared (for example /tmp or a mounted volume) and must
		// never be chmodded as a side effect of opening one database.
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("store: create db directory: %w", err)
		}
	}

	// modernc.org/sqlite runs each _pragma query parameter as a PRAGMA
	// statement on every new pool connection. Without a "file:" prefix the
	// driver uses everything before '?' verbatim as the filesystem path (no
	// URL decoding), which keeps paths containing spaces or '%' safe.
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}

	// One connection serializes all access. Under owlwatch's load (one
	// insert per persist tick, occasional history queries) this costs
	// nothing and rules out SQLITE_BUSY between pooled connections.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate schema: %w", err)
	}
	for _, file := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(file, 0o600); err != nil && !os.IsNotExist(err) {
			db.Close()
			return nil, fmt.Errorf("store: secure %s: %w", file, err)
		}
	}
	return &Store{db: db}, nil
}

// Insert flattens one snapshot into the samples and disk_samples tables in a
// single transaction. GPUs are aggregated across cards: avg utilization, sum
// of used memory, max temperature; all NULL when the host has no GPU.
// Re-inserting the same timestamp replaces the previous row, so a stalled
// collector cannot fail the persistence pump.
func (s *Store) Insert(snap metrics.Snapshot) error {
	var gpuUtil, gpuTemp sql.NullFloat64
	var gpuMem sql.NullInt64
	if n := len(snap.GPUs); n > 0 {
		var utilSum float64
		var memSum uint64
		tempMax := snap.GPUs[0].TempC
		for _, g := range snap.GPUs {
			utilSum += g.UtilPct
			memSum += g.MemUsed
			if g.TempC > tempMax {
				tempMax = g.TempC
			}
		}
		gpuUtil = sql.NullFloat64{Float64: utilSum / float64(n), Valid: true}
		gpuMem = sql.NullInt64{Int64: int64(memSum), Valid: true}
		gpuTemp = sql.NullFloat64{Float64: tempMax, Valid: true}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin insert: %w", err)
	}
	defer tx.Rollback() // no-op once committed

	if _, err := tx.Exec(
		`INSERT OR REPLACE INTO samples
		   (ts, cpu_pct, mem_used, mem_pct, swap_used, gpu_util, gpu_mem, gpu_temp)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		snap.TS, snap.CPU.UsagePct, int64(snap.Mem.Used), snap.Mem.UsedPct,
		int64(snap.Mem.SwapUsed), gpuUtil, gpuMem, gpuTemp,
	); err != nil {
		return fmt.Errorf("store: insert sample: %w", err)
	}

	for _, d := range snap.Disks {
		if _, err := tx.Exec(
			`INSERT OR REPLACE INTO disk_samples (ts, mount, used_pct) VALUES (?, ?, ?)`,
			snap.TS, d.Mount, d.UsedPct,
		); err != nil {
			return fmt.Errorf("store: insert disk sample %q: %w", d.Mount, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit insert: %w", err)
	}
	return nil
}

// Query returns bucketed aggregates for the window [now-r.Dur, now], oldest
// first. It is a thin wrapper around QueryContext with a background context.
func (s *Store) Query(r Range, now time.Time) ([]metrics.HistoryPoint, error) {
	return s.QueryContext(context.Background(), r, now)
}

// QueryContext returns bucketed aggregates for the window [now-r.Dur, now],
// oldest first. Each point's TS is the bucket start ((ts / bucketMs) *
// bucketMs). GPU pointer fields are nil for buckets with no GPU data; the
// Disks map is always non-nil. Rows timestamped after now are excluded so a
// corrected forward clock jump cannot leak future samples into every range.
// Cancelling ctx aborts the query — important with SetMaxOpenConns(1), where
// an abandoned long query would otherwise serialize behind newer work.
func (s *Store) QueryContext(ctx context.Context, r Range, now time.Time) ([]metrics.HistoryPoint, error) {
	bucketMs := r.Bucket.Milliseconds()
	if bucketMs <= 0 {
		return nil, fmt.Errorf("store: invalid bucket %v in range %q", r.Bucket, r.Key)
	}
	since := now.Add(-r.Dur).UnixMilli()
	until := now.UnixMilli()

	rows, err := s.db.QueryContext(ctx,
		`SELECT (ts / ?) * ? AS bucket,
		        avg(cpu_pct), max(cpu_pct),
		        avg(mem_used), avg(mem_pct), avg(swap_used),
		        avg(gpu_util), max(gpu_util), avg(gpu_mem), max(gpu_temp)
		 FROM samples
		 WHERE ts >= ? AND ts <= ?
		 GROUP BY ts / ?
		 ORDER BY bucket`,
		bucketMs, bucketMs, since, until, bucketMs,
	)
	if err != nil {
		return nil, fmt.Errorf("store: query samples: %w", err)
	}
	defer rows.Close()

	points := make([]metrics.HistoryPoint, 0, 512)
	for rows.Next() {
		var (
			bucket                               int64
			cpuAvg, cpuMax, memUsed, memPct, swp float64
			gpuUtil, gpuMax, gpuMem, gpuTemp     sql.NullFloat64
		)
		if err := rows.Scan(&bucket, &cpuAvg, &cpuMax, &memUsed, &memPct, &swp,
			&gpuUtil, &gpuMax, &gpuMem, &gpuTemp); err != nil {
			return nil, fmt.Errorf("store: scan sample bucket: %w", err)
		}
		p := metrics.HistoryPoint{
			TS:        bucket,
			CPUPct:    cpuAvg,
			CPUMaxPct: cpuMax,
			MemUsed:   uint64(math.Round(memUsed)),
			MemPct:    memPct,
			SwapUsed:  uint64(math.Round(swp)),
			Disks:     map[string]float64{},
		}
		if gpuUtil.Valid {
			v := gpuUtil.Float64
			p.GPUUtilPct = &v
		}
		if gpuMax.Valid {
			v := gpuMax.Float64
			p.GPUMaxPct = &v
		}
		if gpuMem.Valid {
			v := uint64(math.Round(gpuMem.Float64))
			p.GPUMemUsed = &v
		}
		if gpuTemp.Valid {
			v := gpuTemp.Float64
			p.GPUTempC = &v
		}
		points = append(points, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate sample buckets: %w", err)
	}

	if err := s.mergeDisks(ctx, points, bucketMs, since, until); err != nil {
		return nil, err
	}
	return points, nil
}

// mergeDisks fills each point's Disks map with per-mount bucket averages.
// Disk buckets without a matching samples bucket are dropped — both tables
// are written in one transaction, so orphans only arise from partial prunes.
func (s *Store) mergeDisks(ctx context.Context, points []metrics.HistoryPoint, bucketMs, since, until int64) error {
	if len(points) == 0 {
		return nil
	}
	byTS := make(map[int64]*metrics.HistoryPoint, len(points))
	for i := range points {
		byTS[points[i].TS] = &points[i]
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT (ts / ?) * ? AS bucket, mount, avg(used_pct)
		 FROM disk_samples
		 WHERE ts >= ? AND ts <= ?
		 GROUP BY ts / ?, mount`,
		bucketMs, bucketMs, since, until, bucketMs,
	)
	if err != nil {
		return fmt.Errorf("store: query disk samples: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			bucket int64
			mount  string
			pct    float64
		)
		if err := rows.Scan(&bucket, &mount, &pct); err != nil {
			return fmt.Errorf("store: scan disk bucket: %w", err)
		}
		if p, ok := byTS[bucket]; ok {
			p.Disks[mount] = pct
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: iterate disk buckets: %w", err)
	}
	return nil
}

// Prune deletes all rows older than the given duration from both tables.
func (s *Store) Prune(olderThan time.Duration) error {
	cutoff := time.Now().Add(-olderThan).UnixMilli()

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin prune: %w", err)
	}
	defer tx.Rollback() // no-op once committed

	if _, err := tx.Exec(`DELETE FROM samples WHERE ts < ?`, cutoff); err != nil {
		return fmt.Errorf("store: prune samples: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM disk_samples WHERE ts < ?`, cutoff); err != nil {
		return fmt.Errorf("store: prune disk samples: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit prune: %w", err)
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}
