// Package metrics defines the data types shared by the collector, store and
// HTTP server, and (as JSON) by the web UI.
//
// THIS FILE IS THE CONTRACT. Field names and JSON tags must stay in sync with
// web/src/lib/types.ts. Do not change either file without changing both.
package metrics

// Snapshot is one point-in-time reading of every live metric.
type Snapshot struct {
	TS    int64         `json:"ts"` // unix milliseconds
	CPU   CPUMetrics    `json:"cpu"`
	Mem   MemMetrics    `json:"mem"`
	Disks []DiskMetrics `json:"disks"`
	GPUs  []GPUMetrics  `json:"gpus"` // empty when no GPU is present
}

type CPUMetrics struct {
	UsagePct float64   `json:"usagePct"` // 0-100, all cores combined
	PerCore  []float64 `json:"perCore"`  // 0-100 per logical core
	Load1    float64   `json:"load1"`
	Load5    float64   `json:"load5"`
	Load15   float64   `json:"load15"`
}

type MemMetrics struct {
	Total     uint64  `json:"total"` // bytes
	Used      uint64  `json:"used"`
	Available uint64  `json:"available"`
	UsedPct   float64 `json:"usedPct"`
	SwapTotal uint64  `json:"swapTotal"`
	SwapUsed  uint64  `json:"swapUsed"`
}

type DiskMetrics struct {
	Mount   string  `json:"mount"` // host mount point, e.g. "/"
	Device  string  `json:"device"`
	Fstype  string  `json:"fstype"`
	Total   uint64  `json:"total"` // bytes
	Used    uint64  `json:"used"`
	Free    uint64  `json:"free"`
	UsedPct float64 `json:"usedPct"`
}

type GPUMetrics struct {
	Index    int     `json:"index"`
	Name     string  `json:"name"`
	UtilPct  float64 `json:"utilPct"`
	MemTotal uint64  `json:"memTotal"` // bytes
	MemUsed  uint64  `json:"memUsed"`
	TempC    float64 `json:"tempC"`
	PowerW   float64 `json:"powerW"` // 0 when the driver does not report power
}

// HostInfo is static host identity, served at /api/host and in the SSE hello
// event. Gathered once at startup.
type HostInfo struct {
	Hostname      string   `json:"hostname"`
	OS            string   `json:"os"`       // e.g. "linux", "darwin"
	Platform      string   `json:"platform"` // e.g. "ubuntu 24.04"
	KernelVersion string   `json:"kernelVersion"`
	Arch          string   `json:"arch"`
	CPUModel      string   `json:"cpuModel"`
	CPUCores      int      `json:"cpuCores"` // logical cores
	MemTotal      uint64   `json:"memTotal"` // bytes
	BootTime      int64    `json:"bootTime"` // unix seconds
	HasGPU        bool     `json:"hasGPU"`
	GPUNames      []string `json:"gpuNames"`
	Version       string   `json:"version"` // owlwatch version string
}

// HistoryPoint is one aggregated time bucket returned by /api/history.
// Pointer fields are omitted entirely when the host has no GPU.
type HistoryPoint struct {
	TS         int64              `json:"ts"`     // bucket start, unix ms
	CPUPct     float64            `json:"cpuPct"` // average over the bucket
	CPUMaxPct  float64            `json:"cpuMaxPct"`
	MemUsed    uint64             `json:"memUsed"` // average bytes
	MemPct     float64            `json:"memPct"`
	SwapUsed   uint64             `json:"swapUsed"`
	GPUUtilPct *float64           `json:"gpuUtilPct,omitempty"` // avg across GPUs
	GPUMaxPct  *float64           `json:"gpuMaxPct,omitempty"`  // max across GPUs
	GPUMemUsed *uint64            `json:"gpuMemUsed,omitempty"` // sum across GPUs, avg over bucket
	GPUTempC   *float64           `json:"gpuTempC,omitempty"`   // max across GPUs
	Disks      map[string]float64 `json:"disks"`                // mount -> avg usedPct
}
