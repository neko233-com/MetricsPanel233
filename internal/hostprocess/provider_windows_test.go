//go:build windows

package hostprocess

import (
	"bytes"
	"context"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestHostProcessChildFixture(t *testing.T) {
	if os.Getenv("METRICSPANEL_PROCESS_CHILD_FIXTURE") != "1" {
		return
	}
	time.Sleep(time.Minute)
	os.Exit(0)
}
func TestWindowsActualChildHasPIDParentStartAndCounters(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHostProcessChildFixture$")
	cmd.Env = append(os.Environ(), "METRICSPANEL_PROCESS_CHILD_FIXTURE=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	manager := New()
	detail, err := manager.Get(context.Background(), cmd.Process.Pid, "")
	require.NoError(t, err)
	assert.Equal(t, os.Getpid(), detail.Process.PPID)
	assert.Equal(t, filepath.Base(os.Args[0]), detail.Process.Name)
	assert.Equal(t, "executable", detail.Process.NameSource)
	require.NotEmpty(t, detail.Process.StartToken)
	_, err = strconv.ParseUint(detail.Process.StartToken, 10, 64)
	require.NoError(t, err)
	require.NotNil(t, detail.Process.StartedAt)
	assert.LessOrEqual(t, *detail.Process.StartedAt, time.Now().UnixMilli())
	assert.NotNil(t, detail.Process.CPUSecondsTotal)
	assert.NotNil(t, detail.Process.MemoryRSSBytes)
	assert.Equal(t, "ok", detail.Process.States["memory"])
	stable, err := manager.Get(context.Background(), cmd.Process.Pid, detail.Process.StartToken)
	require.NoError(t, err)
	assert.Equal(t, detail.Process.ID, stable.Process.ID)
	_, err = manager.Get(context.Background(), cmd.Process.Pid, "not-the-process-start")
	assert.ErrorIs(t, err, ErrProcessChanged)
	require.NoError(t, cmd.Process.Kill())
	_ = cmd.Wait()
	_, err = manager.Get(context.Background(), cmd.Process.Pid, detail.Process.StartToken)
	assert.ErrorIs(t, err, ErrProcessNotFound)
}
func windowsResourceCounts(t *testing.T) (uint32, uint32, uint32) {
	t.Helper()
	getGUI := userDLL.NewProc("GetGuiResources")
	process := windows.CurrentProcess()
	gdi, _, _ := getGUI.Call(uintptr(process), 0)
	user, _, _ := getGUI.Call(uintptr(process), 1)
	var handles uint32
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessHandleCount")
	ok, _, err := proc.Call(uintptr(process), uintptr(unsafe.Pointer(&handles)))
	require.NotZero(t, ok, err)
	return uint32(gdi), uint32(user), handles
}
func TestWindowsCommonExecutableRealIconAndHandlesAreReleased(t *testing.T) {
	// Runtime and race-detector thread startup allocates process event handles.
	// Keep that initialization outside the repeated native resource measurement.
	previousProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previousProcs)
	previousGC := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(previousGC)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	executable := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	_, err := os.Stat(executable)
	require.NoError(t, err)
	for i := 0; i < 50; i++ {
		data, source, err := nativeIcon(executable)
		require.NoError(t, err)
		require.Equal(t, "native", source)
		_, err = png.Decode(bytes.NewReader(data))
		require.NoError(t, err)
	}
	beforeGDI, beforeUser, beforeHandles := windowsResourceCounts(t)
	for i := 0; i < 40; i++ {
		data, source, err := nativeIcon(executable)
		require.NoError(t, err)
		require.Equal(t, "native", source)
		img, err := png.Decode(bytes.NewReader(data))
		require.NoError(t, err)
		assert.Equal(t, 32, img.Bounds().Dx())
		assert.Equal(t, 32, img.Bounds().Dy())
		visible := false
		for y := 0; y < 32; y++ {
			for x := 0; x < 32; x++ {
				_, _, _, a := img.At(x, y).RGBA()
				if a > 0 {
					visible = true
				}
			}
		}
		assert.True(t, visible, "real Windows application icon cannot be a transparent placeholder")
		assert.NotEqual(t, genericIcon(), data, "the system application icon must come from its resources")
	}
	afterGDI, afterUser, afterHandles := windowsResourceCounts(t)
	assert.LessOrEqual(t, afterGDI, beforeGDI+1, "DIB and DC handles must be released")
	assert.LessOrEqual(t, afterUser, beforeUser+1, "HICON handles must be destroyed")
	assert.LessOrEqual(t, afterHandles, beforeHandles+2, "process and resource handles must not leak")
	manager := New()
	key := manager.icons.register(executable)
	data, source, err := manager.Icon(context.Background(), key)
	require.NoError(t, err)
	assert.Equal(t, "native", source)
	require.NotEqual(t, genericIcon(), data)
}
