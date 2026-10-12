// Package instance owns the internal physical boundary between organizations.
// It does not authenticate HTTP requests or expose organization selection APIs.
package instance

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

func canonicalRoot(root string) (string, error) {
	if !filepath.IsAbs(root) {
		return "", errors.New("organization root must be an absolute path")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(abs, 0750); err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

func inside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func organizationPath(root string, id int64) (string, error) {
	if id <= 1 {
		return "", errors.New("new organization directories require an ID greater than 1")
	}
	dir := filepath.Join(root, strconv.FormatInt(id, 10))
	if !inside(root, dir) {
		return "", errors.New("organization path leaves the configured root")
	}
	if _, err := os.Lstat(dir); err == nil {
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil || !inside(root, resolved) || !samePath(resolved, dir) {
			return "", errors.New("organization directory is redirected outside its registered path")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(dir, 0750); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || !inside(root, resolved) || !samePath(resolved, dir) {
		return "", errors.New("organization directory does not resolve to its registered path")
	}
	path := filepath.Join(resolved, "control.db")
	for _, file := range []string{path, filepath.Join(resolved, "secrets.key")} {
		if info, err := os.Lstat(file); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("organization database and key must not be symbolic links")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return path, nil
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func ClickHouseDatabase(base string, organizationID int64) (string, error) {
	maxLength := 100
	if organizationID == 1 {
		maxLength = 128
	}
	if organizationID <= 0 || base == "" || len(base) > maxLength {
		return "", errors.New("invalid ClickHouse organization database base")
	}
	for i, ch := range base {
		if !(ch == '_' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || i > 0 && ch >= '0' && ch <= '9') {
			return "", errors.New("ClickHouse database base must be an identifier")
		}
	}
	if organizationID == 1 {
		return base, nil
	}
	return base + "_org_" + strconv.FormatInt(organizationID, 10), nil
}
