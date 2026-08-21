//go:build linux

package collector

import (
	"io/fs"
	"syscall"
)

// diskEntrySize reports allocated blocks on Linux, which reflects disk space
// more faithfully than apparent length for sparse files.
func diskEntrySize(info fs.FileInfo) uint64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Blocks > 0 {
		return uint64(stat.Blocks) * 512
	}
	return uint64(max(info.Size(), 0))
}
