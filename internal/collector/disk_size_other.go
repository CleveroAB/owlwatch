//go:build !linux

package collector

import "io/fs"

func diskEntrySize(info fs.FileInfo) uint64 {
	return uint64(max(info.Size(), 0))
}
