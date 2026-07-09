package collector

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CleveroAB/owlwatch/internal/metrics"
)

const (
	gpuQueryTimeout    = 3 * time.Second
	gpuReprobeInterval = time.Minute
	mib                = 1024 * 1024 // nvidia-smi reports memory in MiB
)

var nvidiaSMIArgs = []string{
	"--query-gpu=index,name,utilization.gpu,memory.total,memory.used,temperature.gpu,power.draw",
	"--format=csv,noheader,nounits",
}

// gpuPoller shells out to nvidia-smi for GPU metrics. When the binary is
// missing or a query fails it marks the GPU unavailable and re-probes at most
// once per gpuReprobeInterval — a driver appearing later is still picked up,
// but a GPU-less host isn't forking a process on every sampling tick.
type gpuPoller struct {
	log *rateLogger

	mu        sync.Mutex
	available bool
	nextProbe time.Time // earliest next probe while unavailable
}

func newGPUPoller(log *rateLogger) *gpuPoller {
	return &gpuPoller{log: log}
}

// probeInitial runs the startup query; its result seeds HostInfo.HasGPU and
// HostInfo.GPUNames. A missing nvidia-smi binary is the normal case on a
// non-NVIDIA host and is not treated as an error worth logging.
func (g *gpuPoller) probeInitial(ctx context.Context) []metrics.GPUMetrics {
	gpus, err := queryNvidiaSMI(ctx)
	if err != nil && !errors.Is(err, exec.ErrNotFound) {
		g.log.printf("gpu", "collector: initial nvidia-smi probe failed: %v", err)
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if err != nil || len(gpus) == 0 {
		g.available = false
		g.nextProbe = time.Now().Add(gpuReprobeInterval)
		return nil
	}
	g.available = true
	return gpus
}

// sample returns the current GPU metrics, or an empty slice when no GPU is
// available. While unavailable it returns immediately between re-probes.
func (g *gpuPoller) sample(ctx context.Context) []metrics.GPUMetrics {
	g.mu.Lock()
	if !g.available && time.Now().Before(g.nextProbe) {
		g.mu.Unlock()
		return []metrics.GPUMetrics{}
	}
	wasAvailable := g.available
	g.mu.Unlock()

	gpus, err := queryNvidiaSMI(ctx)
	if err == nil && len(gpus) == 0 {
		err = errors.New("nvidia-smi reported no GPUs")
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if err != nil {
		if wasAvailable || !errors.Is(err, exec.ErrNotFound) {
			g.log.printf("gpu", "collector: nvidia-smi query failed (re-probing in %s): %v", gpuReprobeInterval, err)
		}
		g.available = false
		g.nextProbe = time.Now().Add(gpuReprobeInterval)
		return []metrics.GPUMetrics{}
	}
	if !wasAvailable {
		g.log.printf("gpu", "collector: nvidia-smi available, GPU metrics enabled")
	}
	g.available = true
	return gpus
}

// queryNvidiaSMI executes one metrics query with a hard timeout so a hung
// driver can never wedge the sampler.
func queryNvidiaSMI(ctx context.Context) ([]metrics.GPUMetrics, error) {
	path, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return nil, err
	}
	qctx, cancel := context.WithTimeout(ctx, gpuQueryTimeout)
	defer cancel()
	out, err := exec.CommandContext(qctx, path, nvidiaSMIArgs...).Output()
	if err != nil {
		return nil, err
	}
	return parseNvidiaSMI(out), nil
}

// parseNvidiaSMI parses `nvidia-smi --format=csv,noheader,nounits` output.
// It is deliberately forgiving: numeric fields may read "[N/A]" or
// "[Not Supported]" (parsed as 0), GPU names may themselves contain commas,
// and malformed lines are skipped rather than failing the whole sample.
func parseNvidiaSMI(out []byte) []metrics.GPUMetrics {
	gpus := []metrics.GPUMetrics{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, ",")
		n := len(fields)
		if n < 7 {
			continue
		}
		// The first field is the index and the last five are numeric, so
		// whatever sits between them is the name (rejoined in case the name
		// itself contains commas).
		name := strings.TrimSpace(strings.Join(fields[1:n-5], ","))
		idx, err := strconv.Atoi(strings.TrimSpace(fields[0]))
		if err != nil {
			idx = len(gpus) // fall back to the line's position
		}
		gpus = append(gpus, metrics.GPUMetrics{
			Index:    idx,
			Name:     name,
			UtilPct:  smiFloat(fields[n-5]),
			MemTotal: uint64(smiFloat(fields[n-4]) * mib),
			MemUsed:  uint64(smiFloat(fields[n-3]) * mib),
			TempC:    smiFloat(fields[n-2]),
			PowerW:   smiFloat(fields[n-1]),
		})
	}
	return gpus
}

// smiFloat parses one nvidia-smi numeric field. Unparseable values —
// "[N/A]", "[Not Supported]", "" — and negatives become 0 per the metrics
// contract (PowerW is 0 when the driver does not report power).
func smiFloat(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
}
