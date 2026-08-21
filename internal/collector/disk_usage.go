package collector

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shirou/gopsutil/v4/disk"

	"github.com/CleveroAB/owlwatch/internal/metrics"
)

const (
	diskBreakdownTimeout  = 30 * time.Second
	diskBreakdownLimit    = 2_000_000
	diskBreakdownItems    = 10
	diskBreakdownWorkers  = 4
	diskBreakdownCacheTTL = 5 * time.Minute
	diskBreakdownCacheCap = 256
)

type diskUsageCacheEntry struct {
	at    time.Time
	usage metrics.DiskUsage
}

var (
	ErrInvalidDiskPath = errors.New("invalid disk path")
	ErrDiskUnavailable = errors.New("disk path unavailable")
)

// DiskUsage measures the immediate children of hostPath recursively and
// returns the largest entries. The walk is intentionally on-demand: doing a
// du-style scan on every collector tick would itself become a resource issue.
func (c *Collector) DiskUsage(ctx context.Context, hostPath string) (metrics.DiskUsage, error) {
	snap, ok := c.Latest()
	if !ok || len(snap.Disks) == 0 {
		return metrics.DiskUsage{}, ErrDiskUnavailable
	}

	if hostPath == "" {
		disks := append([]metrics.DiskMetrics(nil), snap.Disks...)
		sort.Slice(disks, func(i, j int) bool { return disks[i].UsedPct > disks[j].UsedPct })
		hostPath = disks[0].Mount
	}
	hostPath = filepath.Clean(hostPath)
	if !filepath.IsAbs(hostPath) {
		return metrics.DiskUsage{}, fmt.Errorf("%w: path must be absolute", ErrInvalidDiskPath)
	}

	mount, ok := containingDisk(hostPath, snap.Disks)
	if !ok {
		return metrics.DiskUsage{}, fmt.Errorf("%w: path is outside reported filesystems", ErrInvalidDiskPath)
	}
	if cached, ok := c.cachedDiskUsage(hostPath); ok {
		applyMountUsage(&cached, mount)
		return cached, nil
	}
	localPath := hostPathToLocal(c.cfg.Rootfs, hostPath)
	localMount := hostPathToLocal(c.cfg.Rootfs, mount.Mount)
	linkInfo, err := os.Lstat(localPath)
	if err != nil || linkInfo.Mode()&os.ModeSymlink != 0 {
		return metrics.DiskUsage{}, fmt.Errorf("%w: path is not a readable directory", ErrDiskUnavailable)
	}
	resolved, err := filepath.EvalSymlinks(localPath)
	if err != nil {
		return metrics.DiskUsage{}, fmt.Errorf("%w: %v", ErrDiskUnavailable, err)
	}
	resolvedMount, err := filepath.EvalSymlinks(localMount)
	if err != nil {
		return metrics.DiskUsage{}, fmt.Errorf("%w: %v", ErrDiskUnavailable, err)
	}
	if !pathWithin(resolved, resolvedMount) {
		return metrics.DiskUsage{}, fmt.Errorf("%w: symlink escapes filesystem", ErrInvalidDiskPath)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return metrics.DiskUsage{}, fmt.Errorf("%w: path is not a readable directory", ErrDiskUnavailable)
	}

	entries, err := os.ReadDir(resolved)
	if err != nil {
		return metrics.DiskUsage{}, fmt.Errorf("%w: %v", ErrDiskUnavailable, err)
	}
	allMounts := c.allLocalMounts(ctx, snap.Disks)
	result := scanDiskChildren(ctx, resolved, hostPath, entries, allMounts)
	if err := ctx.Err(); err != nil {
		return metrics.DiskUsage{}, err
	}
	applyMountUsage(&result, mount)
	c.storeDiskUsage(hostPath, result)
	return result, nil
}

func applyMountUsage(usage *metrics.DiskUsage, mount metrics.DiskMetrics) {
	usage.Mount = mount.Mount
	usage.MountUsed = mount.Used
	for i := range usage.Items {
		usage.Items[i].UsedPct = 0
		if mount.Used > 0 {
			usage.Items[i].UsedPct = float64(usage.Items[i].Size) / float64(mount.Used) * 100
		}
	}
}

func (c *Collector) cachedDiskUsage(path string) (metrics.DiskUsage, bool) {
	c.diskUsageMu.Lock()
	defer c.diskUsageMu.Unlock()
	entry, ok := c.diskUsageCache[path]
	if !ok || time.Since(entry.at) >= diskBreakdownCacheTTL {
		delete(c.diskUsageCache, path)
		return metrics.DiskUsage{}, false
	}
	entry.usage.Items = append([]metrics.DiskUsageItem(nil), entry.usage.Items...)
	return entry.usage, true
}

func (c *Collector) storeDiskUsage(path string, usage metrics.DiskUsage) {
	c.diskUsageMu.Lock()
	defer c.diskUsageMu.Unlock()
	if c.diskUsageCache == nil {
		c.diskUsageCache = make(map[string]diskUsageCacheEntry)
	}
	if len(c.diskUsageCache) >= diskBreakdownCacheCap {
		var oldestPath string
		var oldest time.Time
		for cachedPath, entry := range c.diskUsageCache {
			if oldestPath == "" || entry.at.Before(oldest) {
				oldestPath, oldest = cachedPath, entry.at
			}
		}
		delete(c.diskUsageCache, oldestPath)
	}
	usage.Items = append([]metrics.DiskUsageItem(nil), usage.Items...)
	c.diskUsageCache[path] = diskUsageCacheEntry{at: time.Now(), usage: usage}
}

func containingDisk(path string, disks []metrics.DiskMetrics) (metrics.DiskMetrics, bool) {
	var best metrics.DiskMetrics
	found := false
	for _, candidate := range disks {
		if pathWithin(path, filepath.Clean(candidate.Mount)) && (!found || len(candidate.Mount) > len(best.Mount)) {
			best, found = candidate, true
		}
	}
	return best, found
}

func pathWithin(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func hostPathToLocal(rootfs, hostPath string) string {
	if rootfs == "" {
		return filepath.Clean(hostPath)
	}
	rel := strings.TrimPrefix(filepath.Clean(hostPath), string(filepath.Separator))
	return filepath.Join(rootfs, rel)
}

// allLocalMounts includes virtual and pseudo filesystems too, so a scan of /
// never descends into /proc, /sys, /dev, or a separately-mounted data disk.
func (c *Collector) allLocalMounts(ctx context.Context, realDisks []metrics.DiskMetrics) map[string]struct{} {
	mounts := make(map[string]struct{})
	for _, item := range realDisks {
		mounts[filepath.Clean(hostPathToLocal(c.cfg.Rootfs, item.Mount))] = struct{}{}
	}
	// Defensive fallback if partition enumeration fails during this request.
	// These are mounted runtime filesystems on Linux and never useful for a
	// "what consumes persistent disk" breakdown.
	for _, path := range []string{"/proc", "/sys", "/dev", "/run"} {
		mounts[filepath.Clean(hostPathToLocal(c.cfg.Rootfs, path))] = struct{}{}
	}
	parts, err := disk.PartitionsWithContext(ctx, true)
	if err != nil {
		return mounts
	}
	for _, part := range parts {
		mounts[filepath.Clean(hostPathToLocal(c.cfg.Rootfs, part.Mountpoint))] = struct{}{}
	}
	return mounts
}

type measuredDiskItem struct {
	item    metrics.DiskUsageItem
	skipped int64
}

func scanDiskChildren(parent context.Context, localPath, hostPath string, entries []os.DirEntry, mounts map[string]struct{}) metrics.DiskUsage {
	ctx, cancel := context.WithTimeout(parent, diskBreakdownTimeout)
	defer cancel()

	var visited atomic.Int64
	var limitHit atomic.Bool
	results := make([]measuredDiskItem, len(entries))
	jobs := make(chan int, len(entries))
	for i := range entries {
		jobs <- i
	}
	close(jobs)

	workers := min(diskBreakdownWorkers, len(entries))
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				entry := entries[i]
				localEntry := filepath.Join(localPath, entry.Name())
				hostEntry := filepath.Join(hostPath, entry.Name())
				if _, mounted := mounts[filepath.Clean(localEntry)]; mounted && filepath.Clean(localEntry) != filepath.Clean(localPath) {
					continue
				}
				results[i] = measureDiskItem(ctx, localEntry, hostEntry, entry, mounts, &visited, &limitHit, cancel)
			}
		}()
	}
	wg.Wait()

	items := make([]metrics.DiskUsageItem, 0, len(results))
	var skipped int64
	for _, measured := range results {
		skipped += measured.skipped
		if measured.item.Path != "" {
			items = append(items, measured.item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Size == items[j].Size {
			return items[i].Name < items[j].Name
		}
		return items[i].Size > items[j].Size
	})
	if len(items) > diskBreakdownItems {
		items = items[:diskBreakdownItems]
	}
	return metrics.DiskUsage{
		Path: hostPath, Items: items, ScannedEntries: visited.Load(),
		SkippedEntries: skipped, Truncated: limitHit.Load() || errors.Is(ctx.Err(), context.DeadlineExceeded),
	}
}

func measureDiskItem(ctx context.Context, localPath, hostPath string, entry os.DirEntry, mounts map[string]struct{}, visited *atomic.Int64, limitHit *atomic.Bool, cancel context.CancelFunc) measuredDiskItem {
	if ctx.Err() != nil || entry.Type()&os.ModeSymlink != 0 {
		return measuredDiskItem{}
	}
	item := metrics.DiskUsageItem{Path: hostPath, Name: entry.Name(), Kind: "file"}
	info, err := entry.Info()
	if err != nil {
		item.Incomplete = true
		return measuredDiskItem{item: item, skipped: 1}
	}
	if !entry.IsDir() {
		if info.Mode().IsRegular() {
			item.Size = diskEntrySize(info)
		}
		visited.Add(1)
		return measuredDiskItem{item: item}
	}
	item.Kind = "directory"
	var size uint64
	var skipped int64
	err = filepath.WalkDir(localPath, func(path string, d fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if visited.Add(1) > diskBreakdownLimit {
			limitHit.Store(true)
			cancel()
			return context.Canceled
		}
		if walkErr != nil {
			skipped++
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if path != localPath {
			if _, mounted := mounts[filepath.Clean(path)]; mounted && d.IsDir() {
				return filepath.SkipDir
			}
		}
		if d.Type().IsRegular() {
			info, infoErr := d.Info()
			if infoErr != nil {
				skipped++
				return nil
			}
			size += diskEntrySize(info)
		}
		return nil
	})
	item.Size = size
	item.Incomplete = err != nil || skipped > 0
	return measuredDiskItem{item: item, skipped: skipped}
}
