//go:build darwin

package hostprocess

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

func nativeRecords(ctx context.Context) ([]record, string, []string, error) {
	const scope = "host"
	if err := ctx.Err(); err != nil {
		return nil, scope, nil, err
	}
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, scope, nil, fmt.Errorf("enumerate processes: %w", err)
	}
	warnings := []string{"CPU, resident memory and thread counts are unavailable in this process provider; executable paths may be restricted by process ownership."}
	if len(processes) > maxProcesses {
		return nil, scope, warnings, errors.New("process count exceeds the 32768-process inventory limit")
	}
	rows := make([]record, 0, len(processes))
	for _, process := range processes {
		if err := ctx.Err(); err != nil {
			return nil, scope, warnings, err
		}
		pid := int(process.Proc.P_pid)
		if pid <= 0 {
			continue
		}
		name := string(bytes.TrimRight(process.Proc.P_comm[:], "\x00"))
		r := record{PID: pid, PPID: int(process.Eproc.Ppid), Name: name, NameSource: "comm", States: map[string]string{
			"name": "ok", "startTime": "unsupported", "cpu": "unsupported", "memory": "unsupported", "threads": "unsupported",
		}}
		start := process.Proc.P_starttime
		if start.Sec > 0 && start.Usec >= 0 && start.Usec < 1000000 {
			started := start.Sec*1000 + int64(start.Usec)/1000
			r.StartedAt = &started
			r.StartToken = strconv.Itoa(pid) + ":" + strconv.FormatInt(start.Sec, 10) + ":" + strconv.FormatInt(int64(start.Usec), 10)
			r.States["startTime"] = "ok"
		}
		data, pathErr := unix.SysctlRaw("kern.procargs2", pid)
		if pathErr == nil && len(data) >= 5 && len(data) <= 1<<20 {
			// The first word is argc, followed by the executable path. Never
			// expose the remaining argument or environment strings.
			if end := bytes.IndexByte(data[4:], 0); end > 0 {
				path := string(data[4 : 4+end])
				if filepath.IsAbs(path) {
					r.Executable, r.Name, r.NameSource = path, filepath.Base(path), "executable"
					r.States["executable"] = "ok"
				}
			}
		}
		if r.Executable == "" {
			switch {
			case errors.Is(pathErr, unix.EPERM), errors.Is(pathErr, unix.EACCES):
				r.States["executable"] = "denied"
			case errors.Is(pathErr, unix.ESRCH):
				r.States["executable"] = "exited"
			default:
				// EINVAL can also mean an ownership restriction, rather than exit.
				r.States["executable"] = "unsupported"
			}
		}
		if r.Name == "" {
			r.Name, r.NameSource = "PID "+strconv.Itoa(pid), "pid"
			r.States["name"] = "unsupported"
		}
		current, currentErr := unix.SysctlKinfoProc("kern.proc.pid", pid)
		if currentErr != nil || current.Proc.P_starttime != process.Proc.P_starttime {
			state := "exited"
			if errors.Is(currentErr, unix.EPERM) || errors.Is(currentErr, unix.EACCES) {
				state = "denied"
			}
			r.Executable, r.StartToken, r.StartedAt = "", "", nil
			for _, field := range []string{"startTime", "executable", "process"} {
				r.States[field] = state
			}
		}
		rows = append(rows, r)
	}
	if err := ctx.Err(); err != nil {
		return nil, scope, warnings, err
	}
	return rows, scope, warnings, nil
}
