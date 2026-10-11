//go:build linux

package hostprocess

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func linuxWriteIconFixture(t *testing.T, path string, width, height int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 200, G: 80, B: 40, A: 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxDesktopExecutableParsing(t *testing.T) {
	for _, test := range []struct{ input, expected string }{
		{`"/opt/My Application/worker" --debug %U`, "/opt/My Application/worker"},
		{`env MODE=prod /usr/bin/worker %f`, "/usr/bin/worker"},
		{`/usr/bin/env MODE="two words" -- worker`, "worker"},
		{`worker %U`, "worker"},
		{`"unterminated`, ""},
		{`env -u HOME worker`, ""},
		{`%f`, ""},
		{``, ""},
	} {
		if got := linuxDesktopExecutable(test.input); got != test.expected {
			t.Errorf("%q: got %q want %q", test.input, got, test.expected)
		}
	}
}

func TestLinuxNativeIconDesktopAndHicolor(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", root)
	t.Setenv("XDG_DATA_DIRS", t.TempDir())
	apps := filepath.Join(root, "applications")
	if err := os.Mkdir(apps, 0700); err != nil {
		t.Fatal(err)
	}
	entry := "[Desktop Entry]\nType=Application\nExec=\"/opt/My Application/worker\" --debug %U\nIcon=worker\n"
	if err := os.WriteFile(filepath.Join(apps, "worker.desktop"), []byte(entry), 0600); err != nil {
		t.Fatal(err)
	}
	theme := filepath.Join(root, "icons", "hicolor")
	linuxWriteIconFixture(t, filepath.Join(theme, "96x64", "apps", "worker.png"), 96, 64)
	if err := os.WriteFile(filepath.Join(theme, "index.theme"), []byte("[Icon Theme]\nDirectories=96x64/apps,../../escape\n"), 0600); err != nil {
		t.Fatal(err)
	}
	data, source, err := nativeIcon("/opt/My Application/worker")
	if err != nil || source != "desktop" || len(data) > 256<<10 {
		t.Fatalf("desktop icon was not resolved: source=%s bytes=%d err=%v", source, len(data), err)
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width > 32 || config.Height > 32 {
		t.Fatalf("icon was not normalized: %+v %v", config, err)
	}
	for _, candidate := range linuxIconCandidates("worker", []string{root}) {
		if strings.Contains(candidate, "escape") {
			t.Fatalf("theme directory escaped its root: %s", candidate)
		}
	}
}

func TestLinuxDesktopAmbiguousAndMalformedEntries(t *testing.T) {
	root := t.TempDir()
	apps := filepath.Join(root, "applications")
	if err := os.Mkdir(apps, 0700); err != nil {
		t.Fatal(err)
	}
	for filename, contents := range map[string]string{
		"a.desktop":      "[Desktop Entry]\nType=Application\nTryExec=/opt/a/worker\nIcon=first\n",
		"b.desktop":      "[Desktop Entry]\nType=Application\nExec=/opt/b/worker\nIcon=second\n",
		"bad.desktop":    "[Desktop Entry]\nType=Application\nExec=\"unterminated\nIcon=bad\n",
		"hidden.desktop": "[Desktop Entry]\nType=Application\nExec=hidden\nIcon=hidden\nHidden=true\n",
		"huge.desktop":   strings.Repeat("x", linuxDesktopFileLimit+1),
	} {
		if err := os.WriteFile(filepath.Join(apps, filename), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	index := buildLinuxDesktopIndex([]string{root})
	if len(index.paths["/opt/a/worker"]) != 1 || len(index.names["worker"]) != 2 || len(index.names["hidden"]) != 0 || len(index.names["unterminated"]) != 0 {
		t.Fatalf("incorrect desktop matching: %+v", index)
	}
	t.Setenv("XDG_DATA_HOME", root)
	t.Setenv("XDG_DATA_DIRS", t.TempDir())
	data, source, err := nativeIcon("/different/path/worker")
	if err != nil || source != "generic" || !bytes.Equal(data, genericIcon()) {
		t.Fatalf("ambiguous basename assigned an application icon: %s %v", source, err)
	}
}

func TestLinuxIconRejectsOversizedAndNonregularInput(t *testing.T) {
	root := t.TempDir()
	hugePNG := filepath.Join(root, "large.png")
	linuxWriteIconFixture(t, hugePNG, 1025, 2)
	if _, err := linuxReadPNG(hugePNG); err == nil {
		t.Fatal("oversized decoded image accepted")
	}
	largeFile := filepath.Join(root, "large.desktop")
	if err := os.WriteFile(largeFile, []byte(strings.Repeat("x", linuxDesktopFileLimit+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := linuxReadRegularFile(largeFile, linuxDesktopFileLimit); err == nil {
		t.Fatal("oversized metadata accepted")
	}
	fifo := filepath.Join(root, "pipe.png")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := linuxReadRegularFile(fifo, linuxIconFileLimit); err == nil {
		t.Fatal("FIFO accepted as icon data")
	}
	if candidates := linuxIconCandidates("../secret", []string{root}); len(candidates) != 0 {
		t.Fatalf("symbolic icon path traversal accepted: %v", candidates)
	}
}
