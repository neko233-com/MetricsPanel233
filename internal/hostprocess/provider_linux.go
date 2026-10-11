//go:build linux

package hostprocess

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/prometheus/procfs"
)

func nativeRecords(ctx context.Context) ([]record, string, []string, error) {
	return linuxRecords(ctx, "/proc")
}

func linuxRecords(ctx context.Context, root string) ([]record, string, []string, error) {
	const scope = "pid_namespace"
	warnings := []string{"Only processes visible through this PID namespace and its procfs permissions are listed."}
	if err := ctx.Err(); err != nil {
		return nil, scope, warnings, err
	}
	fs, err := procfs.NewFS(root)
	if err != nil {
		return nil, scope, warnings, fmt.Errorf("open process filesystem: %w", err)
	}
	processes, err := fs.AllProcs()
	if err != nil {
		return nil, scope, warnings, fmt.Errorf("enumerate processes: %w", err)
	}
	if len(processes) > maxProcesses {
		return nil, scope, warnings, errors.New("process count exceeds the 32768-process inventory limit")
	}
	bootID := linuxBootID(root)
	boot, bootErr := fs.Stat()
	if bootID == "" {
		warnings = append(warnings, "Boot identity is unavailable; process identity tokens are omitted.")
	}
	if bootErr != nil || boot.BootTime == 0 {
		warnings = append(warnings, "Boot time is unavailable; process start timestamps are omitted.")
	}
	rows := make([]record, 0, len(processes))
	for _, process := range processes {
		if err := ctx.Err(); err != nil {
			return nil, scope, warnings, err
		}
		if process.PID <= 0 {
			continue
		}
		r := record{PID: process.PID, States: map[string]string{}}
		stat, statErr := process.Stat()
		if statErr != nil {
			state := linuxReadState(statErr)
			for _, field := range []string{"startTime", "cpu", "memory", "threads"} {
				r.States[field] = state
			}
			if name, err := process.Comm(); err == nil && name != "" {
				r.Name, r.NameSource = name, "comm"
				r.States["name"] = "ok"
			} else {
				r.States["name"] = linuxReadState(err)
			}
		} else {
			r.PPID = stat.PPID
			r.Name, r.NameSource = stat.Comm, "comm"
			r.States["name"] = "ok"
			cpu := stat.CPUTime()
			r.CPUSeconds = &cpu
			r.States["cpu"] = "ok"
			if stat.RSS >= 0 && uint64(stat.RSS) <= math.MaxUint64/uint64(os.Getpagesize()) {
				memory := uint64(stat.RSS) * uint64(os.Getpagesize())
				r.MemoryRSS = &memory
				r.States["memory"] = "ok"
			} else {
				r.States["memory"] = "unsupported"
			}
			if stat.NumThreads >= 0 {
				threads := stat.NumThreads
				r.Threads = &threads
				r.States["threads"] = "ok"
			} else {
				r.States["threads"] = "unsupported"
			}
			if bootID != "" {
				r.StartToken = bootID + ":" + strconv.Itoa(process.PID) + ":" + strconv.FormatUint(stat.Starttime, 10)
			}
			if bootErr == nil && boot.BootTime > 0 && boot.BootTime <= math.MaxInt64/1000 && stat.Starttime/100 <= math.MaxInt64/1000 {
				// procfs uses Linux USER_HZ (100) for both CPUTime and StartTime.
				base := boot.BootTime * 1000
				delta := stat.Starttime/100*1000 + stat.Starttime%100*10
				if delta <= math.MaxInt64-base {
					started := int64(base + delta)
					r.StartedAt = &started
				}
			}
			r.States["startTime"] = "unavailable"
			if r.StartedAt != nil {
				r.States["startTime"] = "ok"
			}
		}
		executable, exeErr := process.Executable()
		if exeErr != nil {
			r.States["executable"] = linuxReadState(exeErr)
		} else if executable == "" {
			r.States["executable"] = "unsupported"
		} else {
			r.Executable = executable
			r.Name = filepath.Base(strings.TrimSuffix(executable, " (deleted)"))
			r.NameSource = "executable"
			r.States["name"], r.States["executable"] = "ok", "ok"
		}
		if statErr == nil {
			// Reading stat again prevents attaching a reused PID's executable to
			// the old process's identity and counters.
			current, currentErr := process.Stat()
			if currentErr != nil || current.Starttime != stat.Starttime {
				state := "exited"
				if currentErr != nil {
					state = linuxReadState(currentErr)
				}
				r.Executable = ""
				r.Name, r.NameSource = stat.Comm, "comm"
				r.StartToken, r.StartedAt = "", nil
				r.CPUSeconds, r.MemoryRSS, r.Threads = nil, nil, nil
				for _, field := range []string{"startTime", "cpu", "memory", "threads", "executable", "process"} {
					r.States[field] = state
				}
			}
		}
		if r.Name == "" {
			r.Name, r.NameSource = "PID "+strconv.Itoa(r.PID), "pid"
		}
		rows = append(rows, r)
	}
	if err := ctx.Err(); err != nil {
		return nil, scope, warnings, err
	}
	return rows, scope, warnings, nil
}

func linuxReadState(err error) string {
	switch {
	case errors.Is(err, os.ErrPermission), errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		return "denied"
	case errors.Is(err, os.ErrNotExist), errors.Is(err, syscall.ESRCH):
		return "exited"
	default:
		return "unsupported"
	}
}

func linuxBootID(root string) string {
	f, err := os.Open(filepath.Join(root, "sys", "kernel", "random", "boot_id"))
	if err != nil {
		return ""
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 129))
	if err != nil || len(data) > 128 {
		return ""
	}
	value := strings.TrimSpace(string(data))
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return ""
	}
	for _, ch := range value {
		if ch != '-' && !(ch >= '0' && ch <= '9') && !(ch >= 'a' && ch <= 'f') && !(ch >= 'A' && ch <= 'F') {
			return ""
		}
	}
	return strings.ToLower(value)
}
