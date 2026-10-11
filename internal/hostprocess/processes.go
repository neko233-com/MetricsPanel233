package hostprocess

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/neko233-com/MetricsPanel233/internal/model"
)

var (
	ErrInvalidQuery    = errors.New("invalid process query")
	ErrSnapshotExpired = errors.New("process snapshot expired; start a new list")
	ErrProcessNotFound = errors.New("process no longer exists or is not visible")
	ErrProcessChanged  = errors.New("PID now refers to a different process")
	ErrIconNotFound    = errors.New("process icon key not found")
)

const (
	SnapshotTTL  = 60 * time.Second
	maxSnapshots = 8
	maxProcesses = 32768
	DefaultLimit = 200
	MaxLimit     = 1000
)

type record struct {
	PID, PPID                                int
	Name, NameSource, Executable, StartToken string
	StartedAt                                *int64
	CPUSeconds                               *float64
	MemoryRSS                                *uint64
	Threads                                  *int
	States                                   map[string]string
}
type Query struct {
	Prefix              string
	Limit               int
	Continue            string
	PrefixSet, LimitSet bool
}
type sample struct {
	cpu float64
	at  time.Time
}
type snapshot struct {
	id, prefix string
	limit      int
	at         time.Time
	items      []model.HostProcess
	source     model.HostProcessSource
	partial    bool
	warnings   []string
}
type Manager struct {
	mu           sync.Mutex
	enumerateMu  sync.Mutex
	nextSnapshot uint64
	secret       [32]byte
	source       model.HostProcessSource
	now          func() time.Time
	provider     func(context.Context) ([]record, string, []string, error)
	snapshots    map[string]*snapshot
	previous     map[string]sample
	icons        *iconCache
}

func New() *Manager {
	m := &Manager{now: time.Now, provider: nativeRecords, snapshots: map[string]*snapshot{}, previous: map[string]sample{}, icons: newIconCache()}
	if _, err := rand.Read(m.secret[:]); err != nil {
		panic("process identity entropy unavailable: " + err.Error())
	}
	host, _ := os.Hostname()
	m.source = model.HostProcessSource{ID: m.digest("server"), Hostname: host, OS: runtime.GOOS, Scope: "host", Capabilities: model.HostProcessCapabilities{CPU: runtime.GOOS == "windows" || runtime.GOOS == "linux", Memory: runtime.GOOS == "windows" || runtime.GOOS == "linux", Icons: runtime.GOOS == "windows" || runtime.GOOS == "linux"}}
	return m
}
func (m *Manager) digest(value string) string {
	mac := hmac.New(sha256.New, m.secret[:])
	_, _ = mac.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:18])
}
func canonicalPrefix(s string) (string, error) {
	if !utf8.ValidString(s) || len(s) > 128 || strings.ContainsAny(s, "\x00\r\n") {
		return "", fmt.Errorf("%w: prefix must be valid UTF-8, at most 128 bytes", ErrInvalidQuery)
	}
	return strings.ToLower(s), nil
}
func (m *Manager) capture(ctx context.Context, prefix string, limit int) (*snapshot, error) {
	m.enumerateMu.Lock()
	defer m.enumerateMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, scope, warnings, err := m.provider(ctx)
	if err != nil {
		return nil, err
	}
	if len(rows) > maxProcesses {
		return nil, errors.New("process count exceeds the 32768-process inventory limit")
	}
	at := m.now()
	m.nextSnapshot++
	id := m.digest(fmt.Sprintf("snapshot:%d:%d", at.UnixNano(), m.nextSnapshot))
	s := &snapshot{id: id, prefix: prefix, limit: limit, at: at, source: m.source, items: []model.HostProcess{}, warnings: append([]string{}, warnings...)}
	s.source.Scope = scope
	next := map[string]sample{}
	for _, r := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		states := map[string]string{"startTime": "unavailable", "cpu": "unavailable", "memory": "unavailable"}
		for k, v := range r.States {
			states[k] = v
		}

		if r.States["process"] == "exited" {
			r.StartToken, r.Executable = "", ""
			r.StartedAt, r.CPUSeconds, r.MemoryRSS, r.Threads = nil, nil, nil, nil
			for _, field := range []string{"startTime", "cpu", "memory", "threads"} {
				states[field] = "exited"
			}
		}
		if r.States["cpu"] == "exited" {
			r.CPUSeconds = nil
		}
		if r.States["memory"] == "exited" {
			r.MemoryRSS = nil
		}

		identity := m.digest(fmt.Sprintf("process:%d:%s", r.PID, r.StartToken))
		if r.StartToken == "" {
			identity = m.digest(fmt.Sprintf("uncertain:%s:%d", id, r.PID))
		}
		p := model.HostProcess{ID: identity, PID: r.PID, PPID: r.PPID, Name: r.Name, NameSource: r.NameSource, StartToken: r.StartToken, StartedAt: r.StartedAt, CPUSecondsTotal: r.CPUSeconds, Threads: r.Threads, IconKey: m.icons.register(r.Executable), States: states}
		if r.MemoryRSS != nil {
			v := strconv.FormatUint(*r.MemoryRSS, 10)
			p.MemoryRSSBytes = &v
			states["memory"] = "ok"
		}
		if r.StartedAt != nil {
			states["startTime"] = "ok"
		}
		if r.CPUSeconds != nil && !math.IsNaN(*r.CPUSeconds) && !math.IsInf(*r.CPUSeconds, 0) {
			states["cpu"] = "warming_up"
			if r.StartToken != "" {
				if prev, ok := m.previous[identity]; ok && at.After(prev.at) && *r.CPUSeconds >= prev.cpu {
					v := (*r.CPUSeconds - prev.cpu) / at.Sub(prev.at).Seconds() * 100
					p.CPUPercent = &v
					states["cpu"] = "ok"
				}
				next[identity] = sample{cpu: *r.CPUSeconds, at: at}
			}
		}
		if states["startTime"] != "ok" || states["memory"] == "denied" || states["memory"] == "exited" || states["cpu"] == "denied" || states["cpu"] == "exited" {
			s.partial = true
		}
		if strings.HasPrefix(strings.ToLower(p.Name), prefix) {
			s.items = append(s.items, cloneProcess(p))
		}
	}
	m.previous = next
	sort.SliceStable(s.items, func(i, j int) bool {
		a, b := strings.ToLower(s.items[i].Name), strings.ToLower(s.items[j].Name)
		if a != b {
			return a < b
		}
		return s.items[i].PID < s.items[j].PID
	})
	return s, nil
}
func (m *Manager) List(ctx context.Context, q Query) (model.HostProcessList, error) {
	prefix, err := canonicalPrefix(q.Prefix)
	if err != nil {
		return model.HostProcessList{}, err
	}
	limit := q.Limit
	if limit == 0 && !q.LimitSet {
		limit = DefaultLimit
	}
	if q.LimitSet && (limit < 1 || limit > MaxLimit) {
		return model.HostProcessList{}, fmt.Errorf("%w: limit must be 1–1000", ErrInvalidQuery)
	}
	if q.Continue != "" {
		parts := strings.Split(q.Continue, ".")
		if len(parts) != 3 || !hmac.Equal([]byte(parts[2]), []byte(m.digest("page:"+parts[0]+"."+parts[1]))) {
			return model.HostProcessList{}, fmt.Errorf("%w: invalid continuation token", ErrInvalidQuery)
		}
		offset, err := strconv.Atoi(parts[1])
		if err != nil || offset < 1 {
			return model.HostProcessList{}, ErrInvalidQuery
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		s := m.snapshots[parts[0]]
		if s == nil || !m.now().Before(s.at.Add(SnapshotTTL)) {
			delete(m.snapshots, parts[0])
			return model.HostProcessList{}, ErrSnapshotExpired
		}
		if (q.PrefixSet && prefix != s.prefix) || (q.LimitSet && limit != s.limit) || offset >= len(s.items) {
			return model.HostProcessList{}, fmt.Errorf("%w: continuation must retain prefix and limit", ErrInvalidQuery)
		}
		return m.page(s, offset), nil
	}
	if limit < 1 || limit > MaxLimit {
		return model.HostProcessList{}, ErrInvalidQuery
	}
	s, err := m.capture(ctx, prefix, limit)
	if err != nil {
		return model.HostProcessList{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, old := range m.snapshots {
		if !m.now().Before(old.at.Add(SnapshotTTL)) {
			delete(m.snapshots, id)
		}
	}
	for len(m.snapshots) >= maxSnapshots {
		var oldest string
		var age time.Time
		for id, old := range m.snapshots {
			if oldest == "" || old.at.Before(age) {
				oldest, age = id, old.at
			}
		}
		delete(m.snapshots, oldest)
	}
	m.snapshots[s.id] = s
	return m.page(s, 0), nil
}
func (m *Manager) page(s *snapshot, offset int) model.HostProcessList {
	end := min(offset+s.limit, len(s.items))
	token := ""
	if end < len(s.items) {
		anchor := s.id + "." + strconv.Itoa(end)
		token = anchor + "." + m.digest("page:"+anchor)
	}
	items := make([]model.HostProcess, 0, end-offset)
	for _, p := range s.items[offset:end] {
		items = append(items, cloneProcess(p))
	}
	return model.HostProcessList{Source: s.source, SnapshotID: s.id, SampledAt: s.at.UnixMilli(), ExpiresAt: s.at.Add(SnapshotTTL).UnixMilli(), Prefix: s.prefix, Order: "name_asc_pid_asc", Total: len(s.items), Items: items, ContinueToken: token, Partial: s.partial, Warnings: append([]string{}, s.warnings...)}
}
func (m *Manager) Get(ctx context.Context, pid int, startToken string) (model.HostProcessDetail, error) {
	if pid < 0 || uint64(pid) > 4294967295 || len(startToken) > 256 {
		return model.HostProcessDetail{}, ErrInvalidQuery
	}
	s, err := m.capture(ctx, "", DefaultLimit)
	if err != nil {
		return model.HostProcessDetail{}, err
	}
	for _, p := range s.items {
		if p.PID != pid || p.States["process"] == "exited" {
			continue
		}
		if startToken != "" && startToken != p.StartToken {
			return model.HostProcessDetail{}, ErrProcessChanged
		}
		return model.HostProcessDetail{Source: s.source, SampledAt: s.at.UnixMilli(), Process: p}, nil
	}
	return model.HostProcessDetail{}, ErrProcessNotFound
}
func (m *Manager) Icon(ctx context.Context, key string) ([]byte, string, error) {
	return m.icons.get(ctx, key)
}

func cloneProcess(p model.HostProcess) model.HostProcess {
	p.StartedAt = clonePointer(p.StartedAt)
	p.CPUSecondsTotal = clonePointer(p.CPUSecondsTotal)
	p.CPUPercent = clonePointer(p.CPUPercent)
	p.MemoryRSSBytes = clonePointer(p.MemoryRSSBytes)
	p.Threads = clonePointer(p.Threads)
	states := map[string]string{}
	for k, v := range p.States {
		states[k] = v
	}
	p.States = states
	return p
}
func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
