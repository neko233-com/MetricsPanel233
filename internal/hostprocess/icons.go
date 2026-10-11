package hostprocess

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"strconv"
	"sync"
	"time"
)

const maxIconBytes = 256 * 1024
const maxIconEntries = 256
const maxIconCacheBytes = 8 * 1024 * 1024
const maxIconRegistrations = maxProcesses * maxSnapshots

type iconEntry struct {
	mu      sync.Mutex
	path    string
	expires time.Time
}
type iconImage struct {
	data    []byte
	source  string
	touched time.Time
}
type iconCache struct {
	mu        sync.Mutex
	entries   map[string]*iconEntry
	paths     map[string]string
	images    map[string]iconImage
	bytes     int
	lastSweep time.Time
	now       func() time.Time
	slots     chan struct{}
}

func newIconCache() *iconCache {
	return &iconCache{entries: map[string]*iconEntry{}, paths: map[string]string{}, images: map[string]iconImage{}, now: time.Now, slots: make(chan struct{}, 4)}
}
func (c *iconCache) sweep(now time.Time) {
	if now.Before(c.lastSweep.Add(time.Second)) {
		return
	}
	c.lastSweep = now
	for key, e := range c.entries {
		if !now.Before(e.expires) {
			delete(c.entries, key)
			if cached, ok := c.images[key]; ok {
				c.bytes -= len(cached.data)
				delete(c.images, key)
			}
		}
	}
	for fingerprint, key := range c.paths {
		if c.entries[key] == nil {
			delete(c.paths, fingerprint)
		}
	}
}
func (c *iconCache) register(path string) string {
	if path == "" {
		return "generic"
	}
	stat, err := os.Stat(path)
	if err != nil || !stat.Mode().IsRegular() {
		return "generic"
	}
	fingerprint := path + "\x00" + stat.ModTime().String() + "\x00" + strconv.FormatInt(stat.Size(), 10)
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweep(now)
	if key := c.paths[fingerprint]; key != "" {
		if e := c.entries[key]; e != nil {
			e.expires = now.Add(SnapshotTTL)
			return key
		}
		delete(c.paths, fingerprint)
	}
	if len(c.entries) >= maxIconRegistrations {
		return "generic"
	}
	var keyBytes [18]byte
	if _, err := rand.Read(keyBytes[:]); err != nil {
		return "generic"
	}
	key := base64.RawURLEncoding.EncodeToString(keyBytes[:])
	c.entries[key] = &iconEntry{path: path, expires: now.Add(SnapshotTTL)}
	c.paths[fingerprint] = key
	return key
}
func (c *iconCache) cached(key string, now time.Time) ([]byte, string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value, ok := c.images[key]
	if !ok {
		return nil, "", false
	}
	value.touched = now
	c.images[key] = value
	return append([]byte(nil), value.data...), value.source, true
}
func (c *iconCache) get(ctx context.Context, key string) ([]byte, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if key == "generic" {
		return genericIcon(), "generic", nil
	}
	now := c.now()
	c.mu.Lock()
	e := c.entries[key]
	expired := e != nil && !now.Before(e.expires)
	c.mu.Unlock()
	if e == nil || expired {
		return nil, "", ErrIconNotFound
	}
	if data, source, ok := c.cached(key, now); ok {
		return data, source, nil
	}
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if data, source, ok := c.cached(key, c.now()); ok {
		return data, source, nil
	}
	data, source, err := nativeIcon(e.path)
	if err != nil || len(data) == 0 || len(data) > maxIconBytes {
		data, source = genericIcon(), "generic"
	}
	if cfg, err := png.DecodeConfig(bytes.NewReader(data)); err != nil || cfg.Width > 64 || cfg.Height > 64 || cfg.Width < 1 || cfg.Height < 1 {
		data, source = genericIcon(), "generic"
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	c.mu.Lock()
	for len(c.images) >= maxIconEntries || c.bytes+len(data) > maxIconCacheBytes {
		var oldkey string
		var oldest time.Time
		for k, value := range c.images {
			if oldkey == "" || value.touched.Before(oldest) {
				oldkey, oldest = k, value.touched
			}
		}
		if oldkey == "" {
			break
		}
		c.bytes -= len(c.images[oldkey].data)
		delete(c.images, oldkey)
	}
	c.images[key] = iconImage{data: append([]byte(nil), data...), source: source, touched: c.now()}
	c.bytes += len(data)
	c.mu.Unlock()
	return data, source, nil
}

var genericOnce sync.Once
var genericPNG []byte

func genericIcon() []byte {
	genericOnce.Do(func() {
		img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
		for y := 5; y < 27; y++ {
			for x := 4; x < 28; x++ {
				c := color.NRGBA{R: 92, G: 111, B: 137, A: 255}
				if x > 6 && x < 25 && y > 8 && y < 23 {
					c = color.NRGBA{R: 24, G: 34, B: 49, A: 255}
				}
				img.SetNRGBA(x, y, c)
			}
		}
		for x := 8; x < 14; x++ {
			img.SetNRGBA(x, 13, color.NRGBA{R: 172, G: 205, B: 236, A: 255})
		}
		var b bytes.Buffer
		_ = png.Encode(&b, img)
		genericPNG = b.Bytes()
	})
	return append([]byte(nil), genericPNG...)
}

// normalizePNG bounds input bytes and dimensions before decoding image pixels.
func normalizePNG(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxIconBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxIconBytes {
		return nil, ErrIconNotFound
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 1024 || cfg.Height > 1024 {
		return nil, ErrIconNotFound
	}
	src, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			img.Set(x, y, src.At(src.Bounds().Min.X+x*cfg.Width/32, src.Bounds().Min.Y+y*cfg.Height/32))
		}
	}
	var out bytes.Buffer
	err = png.Encode(&out, img)
	return out.Bytes(), err
}
