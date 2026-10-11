//go:build windows

package hostprocess

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"unsafe"

	"golang.org/x/sys/windows"
)

var processMemoryInfo = windows.NewLazySystemDLL("kernel32.dll").NewProc("K32GetProcessMemoryInfo")

type memoryCounters struct {
	Size, PageFaultCount                               uint32
	PeakWorkingSetSize, WorkingSetSize                 uintptr
	QuotaPeakPagedPoolUsage, QuotaPagedPoolUsage       uintptr
	QuotaPeakNonPagedPoolUsage, QuotaNonPagedPoolUsage uintptr
	PagefileUsage, PeakPagefileUsage                   uintptr
}

func windowsReadState(err error) string {
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return "denied"
	}
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) || errors.Is(err, windows.ERROR_NOT_FOUND) {
		return "exited"
	}
	return "unavailable"
}
func nativeRecords(ctx context.Context) ([]record, string, []string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "host", nil, err
	}
	handle, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, "host", nil, fmt.Errorf("enumerate Windows processes: %w", err)
	}
	defer windows.CloseHandle(handle)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	err = windows.Process32First(handle, &entry)
	rows := []record{}
	for err == nil {
		if e := ctx.Err(); e != nil {
			return nil, "host", nil, e
		}
		r := record{PID: int(entry.ProcessID), PPID: int(entry.ParentProcessID), Name: windows.UTF16ToString(entry.ExeFile[:]), NameSource: "snapshot", States: map[string]string{"startTime": "unavailable", "cpu": "unavailable", "memory": "unavailable", "executable": "unavailable", "threads": "ok"}}
		threads := int(entry.Threads)
		r.Threads = &threads
		process, openErr := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, entry.ProcessID)
		if openErr != nil {
			state := windowsReadState(openErr)
			r.States["startTime"], r.States["cpu"], r.States["memory"], r.States["executable"] = state, state, state, state
			if entry.ProcessID == 0 {
				r.States["startTime"], r.States["cpu"], r.States["memory"] = "unsupported", "unsupported", "unsupported"
			}
		} else {
			func() {
				defer windows.CloseHandle(process)
				buffer := make([]uint16, 32768)
				size := uint32(len(buffer))
				if e := windows.QueryFullProcessImageName(process, 0, &buffer[0], &size); e == nil {
					r.Executable = windows.UTF16ToString(buffer[:size])
					r.Name = filepath.Base(r.Executable)
					r.NameSource = "executable"
					r.States["executable"] = "ok"
				} else {
					r.States["executable"] = windowsReadState(e)
				}
				var created, exited, kernel, user windows.Filetime
				if e := windows.GetProcessTimes(process, &created, &exited, &kernel, &user); e == nil {
					raw := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
					if raw > 0 {
						r.StartToken = strconv.FormatUint(raw, 10)
						const unixEpoch uint64 = 116444736000000000
						if raw >= unixEpoch && raw-unixEpoch <= math.MaxInt64 {
							ms := int64((raw - unixEpoch) / 10000)
							r.StartedAt = &ms
						}
						r.States["startTime"] = "ok"
					}
					seconds := float64(uint64(kernel.HighDateTime)<<32|uint64(kernel.LowDateTime))/1e7 + float64(uint64(user.HighDateTime)<<32|uint64(user.LowDateTime))/1e7
					r.CPUSeconds = &seconds
					r.States["cpu"] = "ok"
					if exited.HighDateTime != 0 || exited.LowDateTime != 0 {
						r.States["process"] = "exited"
					}
				} else {
					state := windowsReadState(e)
					r.States["startTime"], r.States["cpu"] = state, state
				}
				counts := memoryCounters{Size: uint32(unsafe.Sizeof(memoryCounters{}))}
				if e := processMemoryInfo.Find(); e != nil {
					r.States["memory"] = "unsupported"
				} else if result, _, e := processMemoryInfo.Call(uintptr(process), uintptr(unsafe.Pointer(&counts)), uintptr(counts.Size)); result != 0 {
					rss := uint64(counts.WorkingSetSize)
					r.MemoryRSS = &rss
					r.States["memory"] = "ok"
				} else {
					r.States["memory"] = windowsReadState(e)
				}
			}()
		}
		if r.Name == "" {
			r.Name = "PID " + strconv.Itoa(r.PID)
			r.NameSource = "pid"
		}
		rows = append(rows, r)
		if len(rows) > maxProcesses {
			return nil, "host", nil, errors.New("process count exceeds inventory limit")
		}
		entry.Size = uint32(unsafe.Sizeof(entry))
		err = windows.Process32Next(handle, &entry)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, "host", nil, fmt.Errorf("read Windows process snapshot: %w", err)
	}
	return rows, "host", []string{}, nil
}
