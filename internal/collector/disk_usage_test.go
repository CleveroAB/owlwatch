package collector

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/CleveroAB/owlwatch/internal/metrics"
)

func TestDiskUsageRanksActualFilesAndDirectories(t *testing.T) {
	root := t.TempDir()
	mustWriteSizedFile(t, filepath.Join(root, "var", "log", "app.log"), 4<<10)
	mustWriteSizedFile(t, filepath.Join(root, "data", "database.bin"), 12<<10)
	mustWriteSizedFile(t, filepath.Join(root, "archive.tar"), 8<<10)

	c := newTestCollector(1)
	c.cfg.Rootfs = root
	c.publish(metrics.Snapshot{Disks: []metrics.DiskMetrics{{Mount: "/", Used: 40 << 10}}})

	got, err := c.DiskUsage(context.Background(), "/")
	if err != nil {
		t.Fatalf("DiskUsage() error: %v", err)
	}
	if got.Path != "/" || got.Mount != "/" || got.MountUsed != 40<<10 {
		t.Fatalf("DiskUsage() identity = path %q mount %q used %d", got.Path, got.Mount, got.MountUsed)
	}
	if len(got.Items) != 3 {
		t.Fatalf("DiskUsage() returned %d items, want 3: %+v", len(got.Items), got.Items)
	}
	if got.Items[0].Name != "data" || got.Items[0].Kind != "directory" || got.Items[0].Size != 12<<10 || got.Items[0].UsedPct != 30 {
		t.Errorf("largest item = %+v, want data directory using 12 KiB / 30%%", got.Items[0])
	}
	if got.Items[1].Name != "archive.tar" || got.Items[1].Kind != "file" || got.Items[1].Size != 8<<10 {
		t.Errorf("second item = %+v, want archive.tar using 8 KiB", got.Items[1])
	}
}

func TestDiskUsageSupportsDrillDownAndRejectsOtherFilesystems(t *testing.T) {
	root := t.TempDir()
	mustWriteSizedFile(t, filepath.Join(root, "data", "nested", "large.db"), 8<<10)
	mustWriteSizedFile(t, filepath.Join(root, "etc", "config"), 4<<10)

	c := newTestCollector(1)
	c.cfg.Rootfs = root
	c.publish(metrics.Snapshot{Disks: []metrics.DiskMetrics{{Mount: "/data", Used: 16 << 10}}})

	got, err := c.DiskUsage(context.Background(), "/data")
	if err != nil {
		t.Fatalf("DiskUsage(/data) error: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].Path != "/data/nested" || got.Items[0].Size != 8<<10 {
		t.Fatalf("DiskUsage(/data) = %+v, want nested directory", got.Items)
	}

	_, err = c.DiskUsage(context.Background(), "/data/../etc")
	if !errors.Is(err, ErrInvalidDiskPath) {
		t.Fatalf("DiskUsage(path traversal) error = %v, want ErrInvalidDiskPath", err)
	}

	if err := os.Symlink("nested", filepath.Join(root, "data", "link")); err == nil {
		_, err = c.DiskUsage(context.Background(), "/data/link")
		if !errors.Is(err, ErrDiskUnavailable) {
			t.Fatalf("DiskUsage(symlink) error = %v, want ErrDiskUnavailable", err)
		}
	}
}

func TestScanDiskChildrenDoesNotCrossMounts(t *testing.T) {
	root := t.TempDir()
	mustWriteSizedFile(t, filepath.Join(root, "local", "file"), 4<<10)
	mustWriteSizedFile(t, filepath.Join(root, "mounted", "huge"), 8<<10)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}

	got := scanDiskChildren(context.Background(), root, "/", entries, map[string]struct{}{
		filepath.Join(root, "mounted"): {},
	})
	if len(got.Items) != 1 || got.Items[0].Name != "local" {
		t.Fatalf("scanDiskChildren() items = %+v, want only local", got.Items)
	}
}

func mustWriteSizedFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}
