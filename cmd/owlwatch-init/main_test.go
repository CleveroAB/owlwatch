package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestPrepareDataRepairsTreeWithoutFollowingSymlinks(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	nested := filepath.Join(data, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(nested, "owlwatch.db")
	if err := os.WriteFile(db, []byte("history"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(data, "outside-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	uid, gid := os.Geteuid(), os.Getegid()
	if uid == 0 {
		uid, gid = runUID, runGID
	}
	if err := prepareData(data, uid, gid); err != nil {
		t.Fatalf("prepareData: %v", err)
	}

	for _, path := range []string{data, nested, db, link} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		stat := info.Sys().(*syscall.Stat_t)
		if int(stat.Uid) != uid || int(stat.Gid) != gid {
			t.Errorf("%s owner = %d:%d, want %d:%d", path, stat.Uid, stat.Gid, uid, gid)
		}
	}

	info, err := os.Lstat(outside)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	if int(stat.Uid) != os.Geteuid() || int(stat.Gid) != os.Getegid() {
		t.Errorf("symlink target owner changed to %d:%d", stat.Uid, stat.Gid)
	}
}
