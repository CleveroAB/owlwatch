// Command owlwatch is the single-binary host monitor: it samples host
// metrics, persists history to SQLite and serves the embedded web UI.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/CleveroAB/owlwatch/internal/alerts"
	"github.com/CleveroAB/owlwatch/internal/collector"
	"github.com/CleveroAB/owlwatch/internal/metrics"
	"github.com/CleveroAB/owlwatch/internal/peers"
	"github.com/CleveroAB/owlwatch/internal/server"
	"github.com/CleveroAB/owlwatch/internal/store"
)

// version is stamped at build time via -ldflags "-X main.version=...".
var version = "dev"

type appConfig struct {
	listenAddress   string
	port            int
	dbPath          string
	sampleInterval  time.Duration
	persistInterval time.Duration
	retentionDays   int
	rootfs          string
	allowedHosts    []string
	peers           []peers.Peer // OWLWATCH_PEERS: non-empty makes this instance a hub (DESIGN.md §9)
	token           string       // OWLWATCH_TOKEN: API auth + default outgoing peer token
	maxSSEClients   int
	maxHistory      int
	alerts          alerts.Config // OWLWATCH_SMTP_* / OWLWATCH_ALERT_*: email notifications
}

func main() {
	healthcheck := flag.Bool("healthcheck", false,
		"probe http://127.0.0.1:$OWLWATCH_PORT/healthz and exit 0 (healthy) or 1")
	flag.Parse()

	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	// Docker HEALTHCHECK runs the binary itself; the final image has no
	// shell or curl.
	if *healthcheck {
		os.Exit(runHealthcheck(cfg.port))
	}

	if err := run(cfg); err != nil {
		log.Fatalf("owlwatch: %v", err)
	}
}

func run(cfg appConfig) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(cfg.dbPath)
	if err != nil {
		return fmt.Errorf("open store %s: %w", cfg.dbPath, err)
	}

	col := collector.New(collector.Config{
		SampleInterval: cfg.sampleInterval,
		Rootfs:         cfg.rootfs,
		// RingSize left zero: the collector defaults it to 150 (5 min at 2s).
	})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		col.Run(ctx)
	}()
	go func() {
		defer wg.Done()
		persistLoop(ctx, col, st, cfg.persistInterval, time.Duration(cfg.retentionDays)*24*time.Hour)
	}()

	// Hub mode (DESIGN.md §9): maintain one live connection per peer for as
	// long as we run. Standalone instances get no client at all.
	var peersClient *peers.Client
	if len(cfg.peers) > 0 {
		peersClient = peers.NewClient(cfg.peers)
		wg.Add(1)
		go func() {
			defer wg.Done()
			peersClient.Run(ctx)
		}()
	}

	host := col.HostInfo()
	host.Version = version // HostInfo fills everything except Version (see collector docs)

	var notifier *alerts.Notifier
	if cfg.alerts.Enabled() {
		notifier = alerts.New(cfg.alerts, host.Hostname)
		snaps, unsubscribe := col.Subscribe()
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer unsubscribe()
			notifier.Run(ctx, snaps)
		}()
	}

	gpu := "no"
	if host.HasGPU {
		gpu = "yes"
	}
	auth := "off"
	if cfg.token != "" {
		auth = "on"
	}
	alerting := "off"
	if cfg.alerts.Enabled() {
		alerting = "on"
	}
	log.Printf("owlwatch %s listening on %s (db: %s, gpu: %s, peers: %d, auth: %s, alerts: %s)",
		version, net.JoinHostPort(cfg.listenAddress, strconv.Itoa(cfg.port)), cfg.dbPath, gpu, len(cfg.peers), auth, alerting)

	srv := server.New(server.Config{
		Addr:           net.JoinHostPort(cfg.listenAddress, strconv.Itoa(cfg.port)),
		Collector:      col,
		Store:          st,
		Host:           host,
		SampleInterval: cfg.sampleInterval,
		AllowedHosts:   cfg.allowedHosts,
		Peers:          peersClient,
		Alerts:         notifier,
		Token:          cfg.token,
		MaxSSEClients:  cfg.maxSSEClients,
		MaxHistory:     cfg.maxHistory,
	})
	serveErr := srv.ListenAndServe(ctx)

	// Cancel ctx ourselves (a serve error leaves it live) so the collector
	// and persistence pump exit, then close the store once nothing writes.
	stop()
	wg.Wait()
	if err := st.Close(); err != nil {
		log.Printf("close store: %v", err)
	}
	if serveErr != nil {
		return serveErr
	}
	log.Printf("shutdown complete")
	return nil
}

// persistLoop subscribes to the collector and inserts the freshest snapshot
// into the store on every persist tick, pruning old history on start and
// then hourly.
func persistLoop(ctx context.Context, col *collector.Collector, st *store.Store, persistEvery, retention time.Duration) {
	snaps, unsubscribe := col.Subscribe()
	defer unsubscribe()

	persist := time.NewTicker(persistEvery)
	defer persist.Stop()
	prune := time.NewTicker(time.Hour)
	defer prune.Stop()

	if err := st.Prune(retention); err != nil {
		log.Printf("prune history: %v", err)
	}

	var (
		latest         metrics.Snapshot
		fresh          bool // snapshot arrived since the last insert
		lastInsertFail time.Time
	)
	for {
		select {
		case <-ctx.Done():
			return
		case snap, open := <-snaps:
			if !open {
				return // collector stopped (or dropped us; cannot happen at this cadence)
			}
			latest, fresh = snap, true
		case <-persist.C:
			// Skip ticks without a new sample: snapshot ts is the table's
			// primary key, so re-inserting the same one would error.
			if !fresh {
				continue
			}
			fresh = false
			if err := st.Insert(latest); err != nil && time.Since(lastInsertFail) > time.Minute {
				lastInsertFail = time.Now()
				log.Printf("insert sample: %v", err)
			}
		case <-prune.C:
			if err := st.Prune(retention); err != nil {
				log.Printf("prune history: %v", err)
			}
		}
	}
}

// runHealthcheck implements `owlwatch -healthcheck` for Docker HEALTHCHECK.
// /healthz sits outside the /api/ token gate (DESIGN.md §9.2), so no token
// is needed even when OWLWATCH_TOKEN is set.
func runHealthcheck(port int) int {
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck: status %s\n", resp.Status)
		return 1
	}
	return 0
}

// loadConfig reads the environment variables of DESIGN.md §2, applying
// defaults and rejecting unparseable values.
func loadConfig() (appConfig, error) {
	cfg := appConfig{
		listenAddress:   "127.0.0.1",
		port:            8080,
		dbPath:          "./data/owlwatch.db",
		sampleInterval:  2 * time.Second,
		persistInterval: 10 * time.Second,
		retentionDays:   30,
		rootfs:          os.Getenv("OWLWATCH_ROOTFS"),
		maxSSEClients:   128,
		maxHistory:      16,
	}
	if v := os.Getenv("OWLWATCH_LISTEN"); v != "" {
		if net.ParseIP(v) == nil {
			return cfg, fmt.Errorf("OWLWATCH_LISTEN: want an IP address, got %q", v)
		}
		cfg.listenAddress = v
	}
	if v := os.Getenv("OWLWATCH_PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p < 1 || p > 65535 {
			return cfg, fmt.Errorf("OWLWATCH_PORT: invalid port %q", v)
		}
		cfg.port = p
	}
	if v := os.Getenv("OWLWATCH_DB"); v != "" {
		cfg.dbPath = v
	}
	var err error
	if cfg.sampleInterval, err = envDuration("OWLWATCH_SAMPLE_INTERVAL", cfg.sampleInterval); err != nil {
		return cfg, err
	}
	if cfg.persistInterval, err = envDuration("OWLWATCH_PERSIST_INTERVAL", cfg.persistInterval); err != nil {
		return cfg, err
	}
	if cfg.sampleInterval < 250*time.Millisecond || cfg.sampleInterval > time.Minute {
		return cfg, fmt.Errorf("OWLWATCH_SAMPLE_INTERVAL: want 250ms..1m, got %s", cfg.sampleInterval)
	}
	if cfg.persistInterval < cfg.sampleInterval || cfg.persistInterval > time.Hour {
		return cfg, fmt.Errorf("OWLWATCH_PERSIST_INTERVAL: want sample interval..1h, got %s", cfg.persistInterval)
	}
	if v := os.Getenv("OWLWATCH_RETENTION_DAYS"); v != "" {
		d, err := strconv.Atoi(v)
		if err != nil || d < 1 || d > 3650 {
			return cfg, fmt.Errorf("OWLWATCH_RETENTION_DAYS: want 1..3650, got %q", v)
		}
		cfg.retentionDays = d
	}
	// Host names (beyond IP literals and localhost) the server should answer
	// for; anything else is rejected with 421 to block DNS rebinding.
	for _, name := range strings.Split(os.Getenv("OWLWATCH_ALLOWED_HOSTS"), ",") {
		if name = strings.TrimSpace(name); name != "" {
			cfg.allowedHosts = append(cfg.allowedHosts, name)
		}
	}
	// Federation (DESIGN.md §9): the token gates our own API and is the
	// default outgoing token for peers; peers make this instance a hub. An
	// invalid OWLWATCH_PEERS must be a fatal startup error, never a silent
	// skip.
	cfg.token = os.Getenv("OWLWATCH_TOKEN")
	if cfg.token != "" && len(cfg.token) < 16 {
		return cfg, fmt.Errorf("OWLWATCH_TOKEN: must be at least 16 characters when set")
	}
	if cfg.peers, err = peers.ParsePeers(os.Getenv("OWLWATCH_PEERS"), cfg.token); err != nil {
		return cfg, fmt.Errorf("OWLWATCH_PEERS: %w", err)
	}
	if cfg.maxSSEClients, err = envInt("OWLWATCH_MAX_SSE_CLIENTS", cfg.maxSSEClients, 1, 10000); err != nil {
		return cfg, err
	}
	if cfg.maxHistory, err = envInt("OWLWATCH_MAX_HISTORY_REQUESTS", cfg.maxHistory, 1, 1000); err != nil {
		return cfg, err
	}
	if cfg.alerts, err = loadAlertConfig(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// loadAlertConfig reads the OWLWATCH_SMTP_* / OWLWATCH_ALERT_* variables.
// Alerting is enabled only when SMTP host, sender and recipients are all
// present; a partial configuration is a fatal startup error, never a silent
// skip (same stance as OWLWATCH_PEERS). The threshold defaults are
// suggestions — override any of them, or set one to 0 to disable that rule.
func loadAlertConfig() (alerts.Config, error) {
	cfg := alerts.Config{
		SMTPHost: os.Getenv("OWLWATCH_SMTP_HOST"),
		SMTPUser: os.Getenv("OWLWATCH_SMTP_USER"),
		SMTPPass: os.Getenv("OWLWATCH_SMTP_PASS"),
		From:     os.Getenv("OWLWATCH_SMTP_FROM"),
		CPUPct:   90,
		MemPct:   90,
		DiskPct:  92, // matches the UI's "critical" meter color (DESIGN.md §5.4)
		GPUTempC: 90,
		For:      5 * time.Minute,
		Cooldown: 30 * time.Minute,
	}
	for _, rcpt := range strings.Split(os.Getenv("OWLWATCH_ALERT_TO"), ",") {
		if rcpt = strings.TrimSpace(rcpt); rcpt != "" {
			cfg.To = append(cfg.To, rcpt)
		}
	}
	if cfg.SMTPHost != "" || len(cfg.To) > 0 {
		switch {
		case cfg.SMTPHost == "":
			return cfg, fmt.Errorf("OWLWATCH_SMTP_HOST: required when OWLWATCH_ALERT_TO is set")
		case len(cfg.To) == 0:
			return cfg, fmt.Errorf("OWLWATCH_ALERT_TO: required when OWLWATCH_SMTP_HOST is set")
		case cfg.From == "":
			return cfg, fmt.Errorf("OWLWATCH_SMTP_FROM: required when OWLWATCH_SMTP_HOST is set")
		}
	}
	var err error
	if cfg.SMTPPort, err = envInt("OWLWATCH_SMTP_PORT", 587, 1, 65535); err != nil {
		return cfg, err
	}
	if cfg.CPUPct, err = envFloat("OWLWATCH_ALERT_CPU_PCT", cfg.CPUPct, 0, 100); err != nil {
		return cfg, err
	}
	if cfg.MemPct, err = envFloat("OWLWATCH_ALERT_MEM_PCT", cfg.MemPct, 0, 100); err != nil {
		return cfg, err
	}
	if cfg.DiskPct, err = envFloat("OWLWATCH_ALERT_DISK_PCT", cfg.DiskPct, 0, 100); err != nil {
		return cfg, err
	}
	if cfg.GPUTempC, err = envFloat("OWLWATCH_ALERT_GPU_TEMP_C", cfg.GPUTempC, 0, 150); err != nil {
		return cfg, err
	}
	if cfg.For, err = envDuration("OWLWATCH_ALERT_FOR", cfg.For); err != nil {
		return cfg, err
	}
	if cfg.Cooldown, err = envDuration("OWLWATCH_ALERT_COOLDOWN", cfg.Cooldown); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func envInt(key string, def, min, max int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < min || n > max {
		return def, fmt.Errorf("%s: want %d..%d, got %q", key, min, max, v)
	}
	return n, nil
}

func envFloat(key string, def, min, max float64) (float64, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < min || f > max {
		return def, fmt.Errorf("%s: want %g..%g, got %q", key, min, max, v)
	}
	return f, nil
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return def, fmt.Errorf("%s: want a positive Go duration (e.g. %q), got %q", key, "2s", v)
	}
	return d, nil
}
