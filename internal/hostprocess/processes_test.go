package hostprocess

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSnapshotsKeepCasefoldPIDOrderAndExactMemoryAcrossChangingProvider(t *testing.T) {
	m := New()
	now := time.Unix(1000, 0)
	m.now = func() time.Time { return now }
	rss := uint64(math.MaxUint64)
	seconds := 1.0
	rows := []record{
		{PID: 9, Name: "Alpha", StartToken: "created-9", CPUSeconds: &seconds, MemoryRSS: &rss},
		{PID: 2, Name: "aLPHa", StartToken: "created-2", CPUSeconds: &seconds, MemoryRSS: &rss},
		{PID: 3, Name: "Alpine", StartToken: "created-3", CPUSeconds: &seconds, MemoryRSS: &rss},
		{PID: 1, Name: "Beta", StartToken: "created-1"},
	}
	calls := 0
	m.provider = func(context.Context) ([]record, string, []string, error) { calls++; return rows, "host", nil, nil }
	first, err := m.List(context.Background(), Query{Prefix: "AL", PrefixSet: true, Limit: 1, LimitSet: true})
	require.NoError(t, err)
	require.Len(t, first.Items, 1)
	assert.Equal(t, 2, first.Items[0].PID)
	assert.Equal(t, "al", first.Prefix)
	assert.Equal(t, 3, first.Total)
	assert.Equal(t, "warming_up", first.Items[0].States["cpu"])
	assert.Nil(t, first.Items[0].CPUPercent)
	raw, err := json.Marshal(first)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"memoryRssBytes":"18446744073709551615"`)
	rows = []record{{PID: 500, Name: "A new process"}}
	second, err := m.List(context.Background(), Query{Continue: first.ContinueToken})
	require.NoError(t, err)
	require.Len(t, second.Items, 1)
	assert.Equal(t, 9, second.Items[0].PID)
	assert.Equal(t, first.SnapshotID, second.SnapshotID)
	assert.Equal(t, 1, calls, "later pages must not enumerate a changed process table")
	_, err = m.List(context.Background(), Query{Continue: first.ContinueToken, Prefix: "Beta", PrefixSet: true})
	assert.ErrorIs(t, err, ErrInvalidQuery)
	_, err = m.List(context.Background(), Query{Continue: first.ContinueToken, Limit: 2, LimitSet: true})
	assert.ErrorIs(t, err, ErrInvalidQuery)
	_, err = m.List(context.Background(), Query{Continue: first.ContinueToken + "x"})
	assert.ErrorIs(t, err, ErrInvalidQuery)
	last, err := m.List(context.Background(), Query{Continue: second.ContinueToken})
	require.NoError(t, err)
	assert.Equal(t, 3, last.Items[0].PID)
	assert.Empty(t, last.ContinueToken)
	now = now.Add(SnapshotTTL)
	_, err = m.List(context.Background(), Query{Continue: first.ContinueToken})
	assert.ErrorIs(t, err, ErrSnapshotExpired)
}
func TestProcessRatesVerifyStartTokensAndResetReusedPID(t *testing.T) {
	m := New()
	now := time.Unix(1000, 0)
	m.now = func() time.Time { return now }
	cpu := 1.0
	token := "first"
	m.provider = func(context.Context) ([]record, string, []string, error) {
		copyCPU := cpu
		return []record{{PID: 233, PPID: 1, Name: "worker", StartToken: token, CPUSeconds: &copyCPU, States: map[string]string{"memory": "denied"}}}, "host", nil, nil
	}
	first, err := m.List(context.Background(), Query{})
	require.NoError(t, err)
	assert.Nil(t, first.Items[0].CPUPercent)
	assert.True(t, first.Partial)
	now = now.Add(2 * time.Second)
	cpu = 2
	next, err := m.List(context.Background(), Query{})
	require.NoError(t, err)
	require.NotNil(t, next.Items[0].CPUPercent)
	assert.Equal(t, 50.0, *next.Items[0].CPUPercent)
	assert.Equal(t, first.Items[0].ID, next.Items[0].ID)
	now = now.Add(time.Second)
	token = "new-process"
	cpu = 0
	reused, err := m.List(context.Background(), Query{})
	require.NoError(t, err)
	assert.Nil(t, reused.Items[0].CPUPercent)
	assert.NotEqual(t, first.Items[0].ID, reused.Items[0].ID)
	_, err = m.Get(context.Background(), 233, "first")
	assert.ErrorIs(t, err, ErrProcessChanged)
	detail, err := m.Get(context.Background(), 233, "new-process")
	require.NoError(t, err)
	assert.Equal(t, 233, detail.Process.PID)
	_, err = m.Get(context.Background(), 234, "")
	assert.ErrorIs(t, err, ErrProcessNotFound)
}
func TestInventoryMissingIdentityAndValidation(t *testing.T) {
	m := New()
	m.provider = func(ctx context.Context) ([]record, string, []string, error) {
		if err := ctx.Err(); err != nil {
			return nil, "host", nil, err
		}
		return []record{{PID: 0, Name: "Idle", States: map[string]string{"cpu": "unsupported", "memory": "unsupported", "startTime": "denied"}}}, "host", nil, nil
	}
	first, err := m.List(context.Background(), Query{})
	require.NoError(t, err)
	next, err := m.List(context.Background(), Query{})
	require.NoError(t, err)
	assert.NotEqual(t, first.Items[0].ID, next.Items[0].ID, "unreadable start times cannot claim stable identity")
	assert.Equal(t, "denied", first.Items[0].States["startTime"])
	assert.Nil(t, first.Items[0].CPUPercent)
	for _, q := range []Query{{Limit: 0, LimitSet: true}, {Limit: MaxLimit + 1, LimitSet: true}, {Prefix: strings.Repeat("x", 129)}, {Prefix: "bad\x00"}} {
		_, err := m.List(context.Background(), q)
		assert.ErrorIs(t, err, ErrInvalidQuery)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = m.List(ctx, Query{})
	assert.ErrorIs(t, err, context.Canceled)
	detail, err := m.Get(context.Background(), 0, "")
	require.NoError(t, err)
	assert.Equal(t, 0, detail.Process.PID)
}

func TestExitedProviderRowsCannotKeepLiveIdentityOrCounters(t *testing.T) {
	m := New()
	cpu := 123.0
	memory := uint64(42)
	started := int64(1000)
	threads := 2
	m.provider = func(context.Context) ([]record, string, []string, error) {
		return []record{{PID: 233, Name: "old process", StartToken: "old start", StartedAt: &started, CPUSeconds: &cpu, MemoryRSS: &memory, Threads: &threads, States: map[string]string{"process": "exited", "cpu": "exited", "memory": "exited"}}}, "host", nil, nil
	}
	list, err := m.List(context.Background(), Query{})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	p := list.Items[0]
	assert.Empty(t, p.StartToken)
	assert.Nil(t, p.StartedAt)
	assert.Nil(t, p.CPUSecondsTotal)
	assert.Nil(t, p.CPUPercent)
	assert.Nil(t, p.MemoryRSSBytes)
	assert.Nil(t, p.Threads)
	assert.Equal(t, "exited", p.States["startTime"])
	assert.Equal(t, "exited", p.States["cpu"])
	assert.Equal(t, "exited", p.States["memory"])
	_, err = m.Get(context.Background(), 233, "old start")
	assert.ErrorIs(t, err, ErrProcessNotFound)
}
func TestKnownIdentityWithoutReadableStartedAtKeepsTimeUnavailable(t *testing.T) {
	m := New()
	m.provider = func(context.Context) ([]record, string, []string, error) {
		return []record{{PID: 233, Name: "worker", StartToken: "boot:233:1234", States: map[string]string{"startTime": "unavailable"}}}, "pid_namespace", nil, nil
	}
	first, err := m.List(context.Background(), Query{})
	require.NoError(t, err)
	assert.NotEmpty(t, first.Items[0].StartToken)
	assert.Nil(t, first.Items[0].StartedAt)
	assert.Equal(t, "unavailable", first.Items[0].States["startTime"])
}

func TestSnapshotIDsAreUniqueAtTheSameClockAndOldCursorKeepsItsPage(t *testing.T) {
	m := New()
	now := time.Unix(1000, 0)
	m.now = func() time.Time { return now }
	rows := []record{{PID: 1, Name: "Alpha"}, {PID: 2, Name: "Beta"}}
	m.provider = func(context.Context) ([]record, string, []string, error) { return rows, "host", nil, nil }
	first, err := m.List(context.Background(), Query{Limit: 1, LimitSet: true})
	require.NoError(t, err)
	rows = []record{{PID: 100, Name: "Alpha new"}, {PID: 200, Name: "Beta new"}}
	second, err := m.List(context.Background(), Query{Limit: 1, LimitSet: true})
	require.NoError(t, err)
	assert.NotEqual(t, first.SnapshotID, second.SnapshotID)
	third, err := m.List(context.Background(), Query{Limit: 1, LimitSet: true})
	require.NoError(t, err)
	assert.NotEqual(t, first.SnapshotID, third.SnapshotID)
	assert.NotEqual(t, second.SnapshotID, third.SnapshotID)
	oldPage, err := m.List(context.Background(), Query{Continue: first.ContinueToken})
	require.NoError(t, err)
	require.Len(t, oldPage.Items, 1)
	assert.Equal(t, 2, oldPage.Items[0].PID)
	assert.Equal(t, first.SnapshotID, oldPage.SnapshotID)
}

func TestConcurrentListDetailAndIconReadsKeepSharedStateSafe(t *testing.T) {
	m := New()
	path := filepath.Join(t.TempDir(), "no-icon.bin")
	require.NoError(t, os.WriteFile(path, []byte("no icon"), 0600))
	started := int64(1000)
	cpu := 2.0
	memory := uint64(42)
	m.provider = func(ctx context.Context) ([]record, string, []string, error) {
		if err := ctx.Err(); err != nil {
			return nil, "host", nil, err
		}
		return []record{{PID: 233, Name: "Worker", Executable: path, StartToken: "created", StartedAt: &started, CPUSeconds: &cpu, MemoryRSS: &memory}}, "host", nil, nil
	}
	key := m.icons.register(path)
	var workers sync.WaitGroup
	for i := 0; i < 6; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 5; j++ {
				list, err := m.List(context.Background(), Query{})
				if !assert.NoError(t, err) {
					return
				}
				assert.Equal(t, 233, list.Items[0].PID)
				detail, err := m.Get(context.Background(), 233, "created")
				if !assert.NoError(t, err) {
					return
				}
				assert.Equal(t, list.Items[0].ID, detail.Process.ID)
				_, _, err = m.Icon(context.Background(), key)
				if !assert.NoError(t, err) {
					return
				}
			}
		}()
	}
	workers.Wait()
}
