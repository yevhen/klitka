package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	klitkav1 "github.com/yevhen/klitka/proto/gen/go/klitka/v1"
)

func TestFSRPCMountReadOnlyFlow(t *testing.T) {
	hostDir := t.TempDir()
	filePath := filepath.Join(hostDir, "hello.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	mount := newFSRPCMount(hostDir, klitkav1.MountMode_MOUNT_MODE_RO)
	defer mount.close()

	lookupRes, lookupErr, _ := mount.handle("lookup", map[string]interface{}{
		"parent_ino": uint64(1),
		"name":       "hello.txt",
	})
	if lookupErr != 0 {
		t.Fatalf("lookup err = %d", lookupErr)
	}

	entry := lookupRes["entry"].(map[string]interface{})
	ino := entry["ino"].(uint64)

	openRes, openErr, _ := mount.handle("open", map[string]interface{}{
		"ino":   ino,
		"flags": uint64(0),
	})
	if openErr != 0 {
		t.Fatalf("open err = %d", openErr)
	}
	fh := openRes["fh"].(uint64)

	readRes, readErr, _ := mount.handle("read", map[string]interface{}{
		"fh":     fh,
		"offset": uint64(0),
		"size":   uint64(16),
	})
	if readErr != 0 {
		t.Fatalf("read err = %d", readErr)
	}
	if got := string(readRes["data"].([]byte)); got != "hello" {
		t.Fatalf("read data = %q, want %q", got, "hello")
	}

	_, releaseErr, _ := mount.handle("release", map[string]interface{}{
		"fh": fh,
	})
	if releaseErr != 0 {
		t.Fatalf("release err = %d", releaseErr)
	}

	_, createErr, _ := mount.handle("create", map[string]interface{}{
		"parent_ino": uint64(1),
		"name":       "new.txt",
		"mode":       uint64(0o644),
		"flags":      uint64(0),
	})
	if createErr != linuxErrROFS {
		t.Fatalf("create err = %d, want %d", createErr, linuxErrROFS)
	}
}

func TestFSRPCMountReadWriteFlow(t *testing.T) {
	hostDir := t.TempDir()
	mount := newFSRPCMount(hostDir, klitkav1.MountMode_MOUNT_MODE_RW)
	defer mount.close()

	mkdirRes, mkdirErr, _ := mount.handle("mkdir", map[string]interface{}{
		"parent_ino": uint64(1),
		"name":       "dir",
		"mode":       uint64(0o755),
	})
	if mkdirErr != 0 {
		t.Fatalf("mkdir err = %d", mkdirErr)
	}
	dirEntry := mkdirRes["entry"].(map[string]interface{})
	dirIno := dirEntry["ino"].(uint64)

	createRes, createErr, _ := mount.handle("create", map[string]interface{}{
		"parent_ino": dirIno,
		"name":       "data.txt",
		"mode":       uint64(0o644),
		"flags":      uint64(linuxORDWR),
	})
	if createErr != 0 {
		t.Fatalf("create err = %d", createErr)
	}

	entry := createRes["entry"].(map[string]interface{})
	fileIno := entry["ino"].(uint64)
	fh := createRes["fh"].(uint64)

	_, writeErr, _ := mount.handle("write", map[string]interface{}{
		"fh":     fh,
		"offset": uint64(0),
		"data":   []byte("hello"),
	})
	if writeErr != 0 {
		t.Fatalf("write err = %d", writeErr)
	}

	_, releaseErr, _ := mount.handle("release", map[string]interface{}{"fh": fh})
	if releaseErr != 0 {
		t.Fatalf("release err = %d", releaseErr)
	}

	_, truncateErr, _ := mount.handle("truncate", map[string]interface{}{
		"ino":  fileIno,
		"size": uint64(2),
	})
	if truncateErr != 0 {
		t.Fatalf("truncate err = %d", truncateErr)
	}

	_, renameErr, _ := mount.handle("rename", map[string]interface{}{
		"old_parent_ino": dirIno,
		"old_name":       "data.txt",
		"new_parent_ino": dirIno,
		"new_name":       "renamed.txt",
		"flags":          uint64(0),
	})
	if renameErr != 0 {
		t.Fatalf("rename err = %d", renameErr)
	}

	getattrRes, getattrErr, _ := mount.handle("getattr", map[string]interface{}{"ino": fileIno})
	if getattrErr != 0 {
		t.Fatalf("getattr err = %d", getattrErr)
	}
	attr := getattrRes["attr"].(map[string]interface{})
	if size := attr["size"].(uint64); size != 2 {
		t.Fatalf("unexpected size after truncate+rename: %d", size)
	}

	_, unlinkErr, _ := mount.handle("unlink", map[string]interface{}{
		"parent_ino": dirIno,
		"name":       "renamed.txt",
	})
	if unlinkErr != 0 {
		t.Fatalf("unlink err = %d", unlinkErr)
	}

	if _, err := os.Stat(filepath.Join(hostDir, "dir", "renamed.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected renamed file to be removed, stat err=%v", err)
	}
}

func TestFSRPCMountCloseClosesHandles(t *testing.T) {
	hostDir := t.TempDir()
	filePath := filepath.Join(hostDir, "open.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	mount := newFSRPCMount(hostDir, klitkav1.MountMode_MOUNT_MODE_RW)

	lookupRes, lookupErr, _ := mount.handle("lookup", map[string]interface{}{
		"parent_ino": uint64(1),
		"name":       "open.txt",
	})
	if lookupErr != 0 {
		t.Fatalf("lookup err = %d", lookupErr)
	}
	ino := lookupRes["entry"].(map[string]interface{})["ino"].(uint64)

	openRes, openErr, _ := mount.handle("open", map[string]interface{}{
		"ino":   ino,
		"flags": uint64(linuxORDWR),
	})
	if openErr != 0 {
		t.Fatalf("open err = %d", openErr)
	}
	fh := openRes["fh"].(uint64)

	mount.mu.Lock()
	handle := mount.handles[fh]
	mount.mu.Unlock()
	if handle == nil || handle.file == nil {
		t.Fatal("expected open handle")
	}

	mount.close()

	if _, err := handle.file.Write([]byte("z")); err == nil {
		t.Fatal("expected closed file handle after mount.close")
	}

	mount.mu.Lock()
	remaining := len(mount.handles)
	mount.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("expected no open handles, got %d", remaining)
	}
}

func TestFSRPCMountGCRemovesStaleHandlesAndInodes(t *testing.T) {
	hostDir := t.TempDir()
	mount := newFSRPCMount(hostDir, klitkav1.MountMode_MOUNT_MODE_RW)
	defer mount.close()

	oldMaxHandles := fsrpcMaxHandleEntries
	oldHandleTTL := fsrpcHandleIdleTTL
	oldMaxInodes := fsrpcMaxInodeEntries
	oldInterval := fsrpcGCInterval
	fsrpcMaxHandleEntries = 1
	fsrpcHandleIdleTTL = 25 * time.Millisecond
	fsrpcMaxInodeEntries = 1
	fsrpcGCInterval = 0
	defer func() {
		fsrpcMaxHandleEntries = oldMaxHandles
		fsrpcHandleIdleTTL = oldHandleTTL
		fsrpcMaxInodeEntries = oldMaxInodes
		fsrpcGCInterval = oldInterval
	}()

	for i := 0; i < 3; i++ {
		fileName := filepath.Join(hostDir, "file-gc-"+time.Now().Format("150405.000000")+"-"+string(rune('a'+i))+".txt")
		if err := os.WriteFile(fileName, []byte("x"), 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		lookupRes, lookupErr, _ := mount.handle("lookup", map[string]interface{}{
			"parent_ino": uint64(1),
			"name":       filepath.Base(fileName),
		})
		if lookupErr != 0 {
			t.Fatalf("lookup err = %d", lookupErr)
		}
		ino := lookupRes["entry"].(map[string]interface{})["ino"].(uint64)

		openRes, openErr, _ := mount.handle("open", map[string]interface{}{
			"ino":   ino,
			"flags": uint64(0),
		})
		if openErr != 0 {
			t.Fatalf("open err = %d", openErr)
		}

		if err := os.Remove(fileName); err != nil {
			t.Fatalf("remove fixture: %v", err)
		}

		fh := openRes["fh"].(uint64)
		mount.mu.Lock()
		if handle := mount.handles[fh]; handle != nil {
			handle.lastUsed = time.Now().Add(-time.Second)
		}
		mount.mu.Unlock()
	}

	time.Sleep(30 * time.Millisecond)
	mount.maybeGC(time.Now())

	mount.mu.Lock()
	handleCount := len(mount.handles)
	inodeCount := len(mount.pathToIno)
	mount.mu.Unlock()

	if handleCount != 0 {
		t.Fatalf("expected handles to be GC'd, got %d", handleCount)
	}
	if inodeCount != 1 {
		t.Fatalf("expected only root inode mapping to remain, got %d", inodeCount)
	}
}

func TestFSRPCMountPing(t *testing.T) {
	mount := newFSRPCMount(t.TempDir(), klitkav1.MountMode_MOUNT_MODE_RO)
	defer mount.close()

	res, errCode, message := mount.handle("ping", map[string]interface{}{})
	if errCode != 0 {
		t.Fatalf("ping err = %d (%s)", errCode, message)
	}
	ok, _ := res["ok"].(bool)
	if !ok {
		t.Fatalf("unexpected ping response: %#v", res)
	}
	if got, _ := res["mode"].(string); got != "ro" {
		t.Fatalf("unexpected ping mode: %q", got)
	}
}

func TestFormatFSRPCOpMetrics(t *testing.T) {
	metrics := map[string]fsrpcOpMetrics{
		"read": {
			count:        2,
			errors:       1,
			reqBytes:     30,
			resBytes:     80,
			latencyTotal: 5 * time.Millisecond,
			latencyMax:   3 * time.Millisecond,
		},
		"lookup": {
			count:        1,
			errors:       0,
			reqBytes:     10,
			resBytes:     20,
			latencyTotal: 1 * time.Millisecond,
			latencyMax:   1 * time.Millisecond,
		},
	}

	summary := formatFSRPCOpMetrics(metrics)
	if !strings.Contains(summary, "lookup[count=1 errors=0 avg_ms=1 max_ms=1 req_bytes=10 res_bytes=20]") {
		t.Fatalf("unexpected summary: %s", summary)
	}
	if !strings.Contains(summary, "read[count=2 errors=1 avg_ms=2 max_ms=3 req_bytes=30 res_bytes=80]") {
		t.Fatalf("unexpected summary: %s", summary)
	}
}
