//go:build linux

package hostprocess

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	linuxDesktopEntryLimit = 4096
	linuxDesktopFileLimit  = 64 << 10
	linuxIconFileLimit     = 256 << 10
)

type linuxDesktopIndex struct {
	paths map[string][]string
	names map[string][]string
}

var linuxDesktopCache struct {
	sync.Mutex
	key     string
	expires time.Time
	index   linuxDesktopIndex
}

func nativeIcon(path string) ([]byte, string, error) {
	if !filepath.IsAbs(path) || len(path) > 4096 || strings.ContainsRune(path, 0) {
		return genericIcon(), "generic", nil
	}
	roots := linuxDataRoots()
	index := cachedLinuxDesktopIndex(roots)
	path = filepath.Clean(path)
	icons := index.paths[path]
	if len(icons) == 0 {
		icons = index.names[filepath.Base(path)]
	}
	// Multiple desktop applications can share a launcher (for example,
	// office suites). Do not assign one application's icon to every process.
	if len(icons) != 1 {
		return genericIcon(), "generic", nil
	}
	for _, candidate := range linuxIconCandidates(icons[0], roots) {
		data, err := linuxReadPNG(candidate)
		if err == nil {
			return data, "desktop", nil
		}
	}
	return genericIcon(), "generic", nil
}

func linuxDataRoots() []string {
	var roots []string
	add := func(path string) {
		if !filepath.IsAbs(path) || len(path) > 4096 || len(roots) >= 16 {
			return
		}
		path = filepath.Clean(path)
		for _, existing := range roots {
			if existing == path {
				return
			}
		}
		roots = append(roots, path)
	}
	home := os.Getenv("XDG_DATA_HOME")
	if home == "" {
		if userHome, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(userHome, ".local", "share")
		}
	}
	add(home)
	dirs := os.Getenv("XDG_DATA_DIRS")
	if dirs == "" {
		dirs = "/usr/local/share:/usr/share"
	}
	for _, path := range strings.Split(dirs, string(os.PathListSeparator)) {
		add(path)
	}
	return roots
}

func cachedLinuxDesktopIndex(roots []string) linuxDesktopIndex {
	key := strings.Join(roots, "\x00")
	linuxDesktopCache.Lock()
	defer linuxDesktopCache.Unlock()
	if linuxDesktopCache.key != key || time.Now().After(linuxDesktopCache.expires) {
		linuxDesktopCache.key = key
		linuxDesktopCache.index = buildLinuxDesktopIndex(roots)
		linuxDesktopCache.expires = time.Now().Add(5 * time.Second)
	}
	return linuxDesktopCache.index
}

func buildLinuxDesktopIndex(roots []string) linuxDesktopIndex {
	index := linuxDesktopIndex{paths: map[string][]string{}, names: map[string][]string{}}
	type directory struct {
		path, root string
		depth      int
	}
	remaining := linuxDesktopEntryLimit
	seenIDs := map[string]bool{}
	for _, root := range roots {
		applicationRoot := filepath.Join(root, "applications")
		queue := []directory{{path: applicationRoot, root: applicationRoot}}
		for len(queue) > 0 && remaining > 0 {
			dir := queue[0]
			queue = queue[1:]
			fd, err := unix.Open(dir.path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
			if err != nil {
				continue
			}
			f := os.NewFile(uintptr(fd), dir.path)
			entries, _ := f.ReadDir(remaining + 1)
			f.Close()
			for _, entry := range entries {
				if remaining <= 0 {
					break
				}
				remaining--
				path := filepath.Join(dir.path, entry.Name())
				if entry.IsDir() {
					if dir.depth < 3 {
						queue = append(queue, directory{path: path, root: dir.root, depth: dir.depth + 1})
					}
					continue
				}
				if !strings.HasSuffix(entry.Name(), ".desktop") {
					continue
				}
				rel, err := filepath.Rel(dir.root, path)
				if err != nil {
					continue
				}
				id := strings.ReplaceAll(rel, string(os.PathSeparator), "-")
				if seenIDs[id] {
					continue
				}
				values := linuxINISection(path, "Desktop Entry")
				if values["Hidden"] == "true" {
					seenIDs[id] = true
					continue
				}
				if values["Type"] != "Application" || values["Icon"] == "" {
					continue
				}
				executable := linuxDesktopExecutable(values["TryExec"])
				if executable == "" {
					executable = linuxDesktopExecutable(values["Exec"])
				}
				if executable == "" {
					continue
				}
				seenIDs[id] = true
				icon := linuxDesktopUnescape(values["Icon"])
				if filepath.IsAbs(executable) {
					key := filepath.Clean(executable)
					index.paths[key] = linuxAppendUnique(index.paths[key], icon)
				}
				key := filepath.Base(executable)
				index.names[key] = linuxAppendUnique(index.names[key], icon)
			}
		}
		if remaining <= 0 {
			break
		}
	}
	return index
}

func linuxAppendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func linuxINISection(path, section string) map[string]string {
	values := map[string]string{}
	data, err := linuxReadRegularFile(path, linuxDesktopFileLimit)
	if err != nil {
		return values
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 1024), linuxDesktopFileLimit)
	active := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			active = line == "["+section+"]"
			continue
		}
		if !active || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	if scanner.Err() != nil {
		return map[string]string{}
	}
	return values
}

func linuxDesktopUnescape(value string) string {
	return strings.NewReplacer(`\s`, " ", `\n`, "\n", `\t`, "\t", `\r`, "\r", `\\`, `\`).Replace(value)
}

func linuxDesktopExecutable(value string) string {
	var words []string
	var word strings.Builder
	quoted, escaped, inWord := false, false, false
	flush := func() {
		if inWord {
			words = append(words, word.String())
			word.Reset()
			inWord = false
		}
	}
	for _, ch := range value {
		if escaped {
			word.WriteRune(ch)
			escaped, inWord = false, true
			continue
		}
		switch ch {
		case '\\':
			escaped, inWord = true, true
		case '"':
			quoted, inWord = !quoted, true
		case ' ', '\t':
			if quoted {
				word.WriteRune(ch)
			} else {
				flush()
			}
		default:
			word.WriteRune(ch)
			inWord = true
		}
		if len(words) > 256 {
			return ""
		}
	}
	if quoted || escaped {
		return ""
	}
	flush()
	if len(words) == 0 {
		return ""
	}
	position := 0
	if filepath.Base(words[0]) == "env" {
		position++
		for position < len(words) && strings.Contains(words[position], "=") && !strings.HasPrefix(words[position], "-") {
			position++
		}
		if position < len(words) && words[position] == "--" {
			position++
		}
	}
	if position >= len(words) || words[position] == "" || strings.HasPrefix(words[position], "-") || strings.ContainsAny(words[position], "%\x00\r\n") {
		return ""
	}
	return words[position]
}

func linuxIconCandidates(icon string, roots []string) []string {
	if filepath.IsAbs(icon) {
		if len(icon) > 4096 || strings.ContainsRune(icon, 0) {
			return nil
		}
		return []string{icon}
	}
	if icon == "" || len(icon) > 255 || strings.ContainsAny(icon, "/\\\x00\r\n") || icon == "." || icon == ".." {
		return nil
	}
	name := strings.TrimSuffix(icon, ".png") + ".png"
	var bases []string
	if home, err := os.UserHomeDir(); err == nil && filepath.IsAbs(home) {
		bases = append(bases, filepath.Join(home, ".icons"))
	}
	for _, root := range roots {
		bases = append(bases, filepath.Join(root, "icons"))
	}
	var paths []string
	for _, base := range bases {
		theme := filepath.Join(base, "hicolor")
		dirs := []string{"32x32/apps", "48x48/apps", "64x64/apps", "24x24/apps", "16x16/apps", "128x128/apps"}
		values := linuxINISection(filepath.Join(theme, "index.theme"), "Icon Theme")
		for _, value := range strings.Split(values["Directories"]+","+values["ScaledDirectories"], ",") {
			value = strings.TrimSpace(value)
			if value != "" && filepath.IsLocal(value) && len(value) <= 255 && len(dirs) < 128 {
				dirs = linuxAppendUnique(dirs, value)
			}
		}
		for _, dir := range dirs {
			if len(paths) >= 256 {
				break
			}
			paths = append(paths, filepath.Join(theme, dir, name))
		}
		if len(paths) < 256 {
			paths = append(paths, filepath.Join(base, name))
		}
	}
	for _, root := range roots {
		if len(paths) >= 256 {
			break
		}
		paths = append(paths, filepath.Join(root, "pixmaps", name))
	}
	return paths
}

func linuxReadRegularFile(path string, limit int64) ([]byte, error) {
	// NONBLOCK also protects against metadata pointing to a FIFO or device.
	// f.Stat rejects every nonregular file before bytes are read.
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !stat.Mode().IsRegular() || stat.Size() > limit {
		return nil, errors.New("icon metadata is not a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("icon metadata exceeds its size limit")
	}
	return data, nil
}

func linuxReadPNG(path string) ([]byte, error) {
	data, err := linuxReadRegularFile(path, linuxIconFileLimit)
	if err != nil {
		return nil, err
	}
	return normalizePNG(bytes.NewReader(data))
}
