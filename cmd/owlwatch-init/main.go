// owlwatch-init is the container entrypoint. Some container platforms create
// bind-mounted persistent directories as root:root, hiding the correctly
// owned /data directory from the image. This process repairs that mount while
// privileged, drops permanently to the distroless nonroot user, and then
// replaces itself with owlwatch.
package main

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

const (
	dataDir = "/data"
	runUID  = 65532
	runGID  = 65532
)

func main() {
	if os.Geteuid() == 0 {
		if err := prepareData(dataDir, runUID, runGID); err != nil {
			fatal(err)
		}
		if err := dropPrivileges(runUID, runGID); err != nil {
			fatal(err)
		}
	}

	args := append([]string{"/owlwatch"}, os.Args[1:]...)
	if err := syscall.Exec(args[0], args, os.Environ()); err != nil {
		fatal(fmt.Errorf("exec owlwatch: %w", err))
	}
}

func prepareData(dir string, uid, gid int) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("prepare %s: %w", dir, err)
	}
	// Lchown changes symlinks themselves rather than following a link out of
	// the dedicated data volume.
	if err := chownIfNeeded(dir, uid, gid); err != nil {
		return fmt.Errorf("chown %s: %w", dir, err)
	}
	if err := fs.WalkDir(os.DirFS(dir), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == "." {
			return nil
		}
		if err := chownIfNeeded(dir+"/"+path, uid, gid); err != nil {
			return fmt.Errorf("chown %s: %w", path, err)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("prepare %s contents: %w", dir, err)
	}
	return nil
}

func chownIfNeeded(path string, uid, gid int) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok &&
		int(stat.Uid) == uid && int(stat.Gid) == gid {
		return nil
	}
	return os.Lchown(path, uid, gid)
}

func dropPrivileges(uid, gid int) error {
	if err := syscall.Setgroups([]int{}); err != nil {
		return fmt.Errorf("clear supplementary groups: %w", err)
	}
	if err := syscall.Setgid(gid); err != nil {
		return fmt.Errorf("set gid %d: %w", gid, err)
	}
	if err := syscall.Setuid(uid); err != nil {
		return fmt.Errorf("set uid %d: %w", uid, err)
	}
	return nil
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "owlwatch-init: %v\n", err)
	os.Exit(1)
}
