//go:build windows

package hostprocess

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	shellIcon    = windows.NewLazySystemDLL("shell32.dll").NewProc("ExtractIconExW")
	userDLL      = windows.NewLazySystemDLL("user32.dll")
	destroyIcon  = userDLL.NewProc("DestroyIcon")
	drawIcon     = userDLL.NewProc("DrawIconEx")
	gdiDLL       = windows.NewLazySystemDLL("gdi32.dll")
	createDC     = gdiDLL.NewProc("CreateCompatibleDC")
	deleteDC     = gdiDLL.NewProc("DeleteDC")
	createDIB    = gdiDLL.NewProc("CreateDIBSection")
	selectObject = gdiDLL.NewProc("SelectObject")
	deleteObject = gdiDLL.NewProc("DeleteObject")
)

type bitmapHeader struct {
	Size                         uint32
	Width, Height                int32
	Planes, BitCount             uint16
	Compression, SizeImage       uint32
	XPelsPerMeter, YPelsPerMeter int32
	ColorsUsed, ColorsImportant  uint32
}
type bitmapInfo struct {
	Header bitmapHeader
	Colors [1]uint32
}

func iconCallError(operation string, err error) error {
	if err == nil || errors.Is(err, windows.ERROR_SUCCESS) {
		return errors.New(operation + " failed")
	}
	return fmt.Errorf("%s: %w", operation, err)
}
func renderWindowsIcon(icon uintptr, background byte) ([]byte, error) {
	dc, _, err := createDC.Call(0)
	if dc == 0 {
		return nil, iconCallError("CreateCompatibleDC", err)
	}
	defer deleteDC.Call(dc)
	info := bitmapInfo{Header: bitmapHeader{Size: 40, Width: 32, Height: -32, Planes: 1, BitCount: 32, SizeImage: 32 * 32 * 4}}
	var pixels unsafe.Pointer
	bitmap, _, err := createDIB.Call(dc, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&pixels)), 0, 0)
	if bitmap == 0 || pixels == nil {
		return nil, iconCallError("CreateDIBSection", err)
	}
	defer deleteObject.Call(bitmap)
	old, _, err := selectObject.Call(dc, bitmap)
	if old == 0 || old == ^uintptr(0) {
		return nil, iconCallError("SelectObject", err)
	}
	defer selectObject.Call(dc, old)
	buffer := unsafe.Slice((*byte)(pixels), 32*32*4)
	for i := 0; i < len(buffer); i += 4 {
		buffer[i], buffer[i+1], buffer[i+2], buffer[i+3] = background, background, background, 0
	}
	result, _, err := drawIcon.Call(dc, 0, 0, icon, 32, 32, 0, 0, 3)
	if result == 0 {
		return nil, iconCallError("DrawIconEx", err)
	}
	out := append([]byte(nil), buffer...)
	runtime.KeepAlive(info)
	return out, nil
}
func nativeIcon(path string) ([]byte, string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, "", err
	}
	var icon windows.Handle
	count, _, callErr := shellIcon.Call(uintptr(unsafe.Pointer(name)), 0, 0, uintptr(unsafe.Pointer(&icon)), 1)
	runtime.KeepAlive(name)
	if icon != 0 {
		defer destroyIcon.Call(uintptr(icon))
	}
	if count == 0 || uint32(count) == ^uint32(0) || icon == 0 {
		return nil, "", iconCallError("ExtractIconExW", callErr)
	}
	black, err := renderWindowsIcon(uintptr(icon), 0)
	if err != nil {
		return nil, "", err
	}
	white, err := renderWindowsIcon(uintptr(icon), 255)
	if err != nil {
		return nil, "", err
	}
	hasAlpha := false
	for i := 3; i < len(black); i += 4 {
		if black[i] != 0 {
			hasAlpha = true
			break
		}
	}
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	for i := 0; i < len(black); i += 4 {
		a := int(black[i+3])
		if !hasAlpha {
			difference := max(int(white[i])-int(black[i]), int(white[i+1])-int(black[i+1]), int(white[i+2])-int(black[i+2]))
			a = 255 - min(255, max(0, difference))
		}
		pixel := color.NRGBA{A: uint8(a)}
		if a > 0 {
			pixel.R = uint8(min(255, int(black[i+2])*255/a))
			pixel.G = uint8(min(255, int(black[i+1])*255/a))
			pixel.B = uint8(min(255, int(black[i])*255/a))
		}
		img.SetNRGBA((i/4)%32, (i/4)/32, pixel)
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		return nil, "", err
	}
	return b.Bytes(), "native", nil
}
