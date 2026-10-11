//go:build linux

package hostprocess

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

const linuxFixtureBootID = "1c3c828c-e95d-4a5a-bb71-df768846a4aa"

func writeLinuxProcessFixture(t *testing.T, root string, pid int, start uint64) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	fields := make([]string, 42)
	for i := range fields {
		fields[i] = "0"
	}
	fields[0], fields[1] = "S", "7"
	fields[11], fields[12], fields[17] = "400", "100", "3"
	fields[19], fields[20], fields[21] = strconv.FormatUint(start, 10), "65536", "4"
	stat := fmt.Sprintf("%d (worker (child)) %s\n", pid, strings.Join(fields, " "))
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0600); err != nil {
		t.Fatal(err)
	}
}

func linuxProcessFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	bootDir := filepath.Join(root, "sys", "kernel", "random")
	if err := os.MkdirAll(bootDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bootDir, "boot_id"), []byte(linuxFixtureBootID+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "stat"), []byte("cpu 1 2 3 4 5 6 7 8 9 10\nbtime 1710000000\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestLinuxRecordsExecutableIdentityAndPIDReuse(t *testing.T) {
	root := linuxProcessFixture(t)
	writeLinuxProcessFixture(t, root, 42, 513)
	executable := "/opt/My Application/really-long-worker-name"
	if err := os.Symlink(executable, filepath.Join(root, "42", "exe")); err != nil {
		t.Fatal(err)
	}
	rows, scope, warnings, err := linuxRecords(context.Background(), root)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	r := rows[0]
	if scope != "pid_namespace" || len(warnings) == 0 || r.PID != 42 || r.PPID != 7 {
		t.Fatalf("incorrect scope or identifiers: %s %+v %v", scope, r, warnings)
	}
	if r.Name != "really-long-worker-name" || r.NameSource != "executable" || r.Executable != executable {
		t.Fatalf("truncated or incorrect executable name: %+v", r)
	}
	if r.StartToken != linuxFixtureBootID+":42:513" || r.StartedAt == nil || *r.StartedAt != 1710000005130 {
		t.Fatalf("incorrect start identity or timestamp: %+v", r)
	}
	if r.CPUSeconds == nil || *r.CPUSeconds != 5 || r.MemoryRSS == nil || *r.MemoryRSS != uint64(4*os.Getpagesize()) || r.Threads == nil || *r.Threads != 3 {
		t.Fatalf("incorrect counters: %+v", r)
	}
	for _, field := range []string{"startTime", "cpu", "memory", "threads", "executable"} {
		if r.States[field] != "ok" {
			t.Fatalf("unexpected state %s=%s", field, r.States[field])
		}
	}
	writeLinuxProcessFixture(t, root, 42, 514)
	newRows, _, _, err := linuxRecords(context.Background(), root)
	if err != nil || len(newRows) != 1 || newRows[0].StartToken == r.StartToken {
		t.Fatalf("PID reuse preserved old identity: %+v %v", newRows, err)
	}
}

func TestLinuxRecordsKeepUnavailableProcesses(t *testing.T) {
	root := linuxProcessFixture(t)
	for _, pid := range []string{"43", "44"} {
		if err := os.Mkdir(filepath.Join(root, pid), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "43", "stat"), []byte("malformed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "43", "comm"), []byte("visible worker\n"), 0600); err != nil {
		t.Fatal(err)
	}
	rows, _, _, err := linuxRecords(context.Background(), root)
	if err != nil || len(rows) != 2 {
		t.Fatalf("unavailable processes dropped: %+v %v", rows, err)
	}
	byPID := map[int]record{}
	for _, r := range rows {
		byPID[r.PID] = r
	}
	if r := byPID[43]; r.Name != "visible worker" || r.States["cpu"] != "unsupported" || r.CPUSeconds != nil || r.StartToken != "" {
		t.Fatalf("malformed stat fabricated counters: %+v", r)
	}
	if r := byPID[44]; r.Name != "PID 44" || r.States["startTime"] != "exited" || r.States["memory"] != "exited" {
		t.Fatalf("disappeared process state lost: %+v", r)
	}
}

func TestLinuxRecordsMissingBootMetadata(t *testing.T) {
	root := linuxProcessFixture(t)
	writeLinuxProcessFixture(t, root, 45, 9007199254740993)
	if err := os.Remove(filepath.Join(root, "stat")); err != nil {
		t.Fatal(err)
	}
	rows, _, _, err := linuxRecords(context.Background(), root)
	if err != nil || rows[0].StartToken != linuxFixtureBootID+":45:9007199254740993" || rows[0].StartedAt != nil || rows[0].States["startTime"] != "unavailable" {
		t.Fatalf("raw identity lost without boot timestamp: %+v %v", rows, err)
	}
	if err := os.WriteFile(filepath.Join(root, "stat"), []byte("btime 1710000000\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "sys", "kernel", "random", "boot_id")); err != nil {
		t.Fatal(err)
	}
	rows, _, _, err = linuxRecords(context.Background(), root)
	if err != nil || rows[0].StartToken != "" || rows[0].States["startTime"] != "ok" || rows[0].StartedAt == nil {
		t.Fatalf("timestamp availability incorrectly requires boot identity: %+v %v", rows, err)
	}
}

func TestLinuxRecordsCancellationAndEnumerationError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rows, _, _, err := linuxRecords(ctx, "/nonexistent-procfs")
	if !errors.Is(err, context.Canceled) || rows != nil {
		t.Fatalf("cancellation returned partial or wrong result: %+v %v", rows, err)
	}
	if _, _, _, err := linuxRecords(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing procfs reported as an empty successful inventory")
	}
}

func TestLinuxReadStatePermissionsAndExit(t *testing.T) {
	for _, test := range []struct {
		err   error
		state string
	}{
		{fmt.Errorf("wrapped: %w", syscall.EACCES), "denied"},
		{syscall.EPERM, "denied"},
		{syscall.ESRCH, "exited"},
		{os.ErrNotExist, "exited"},
		{errors.New("malformed"), "unsupported"},
	} {
		if got := linuxReadState(test.err); got != test.state {
			t.Errorf("%v: got %s want %s", test.err, got, test.state)
		}
	}
}
