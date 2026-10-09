package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/analysis"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPatternPersistenceIdempotenceFilteringAndNearestNeighbors(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "patterns.db")
	s, err := store.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { s.DB.Close() })
	patterns := []model.Pattern{}
	for _, example := range []struct {
		job           string
		scale, offset float64
	}{{"reference", 1, 0}, {"scaled", 20, 233}, {"inverse", -1, 63}, {"constant", 0, 10}} {
		series := model.Series{Labels: map[string]string{"job": example.job}}
		for i := 0; i < 64; i++ {
			series.Points = append(series.Points, model.Point{Timestamp: 100000 + int64(i)*1000, Value: float64(i)*example.scale + example.offset})
		}
		p, err := analysis.Embed("orders", series, 100000, 164000, "last", "shape")
		require.NoError(t, err)
		patterns = append(patterns, p)
	}
	for i := 0; i < 3; i++ {
		require.NoError(t, s.SavePatterns(ctx, patterns))
	}
	list, err := s.Patterns(ctx, 100)
	require.NoError(t, err)
	require.Len(t, list, 4)
	assert.Nil(t, list[0].Values)
	hits, err := s.SearchPatterns(ctx, model.PatternSearch{Reference: patterns[0], Limit: 3})
	require.NoError(t, err)
	require.Len(t, hits, 3)
	assert.Equal(t, "scaled", hits[0].Pattern.Labels["job"])
	assert.InDelta(t, 0, hits[0].Distance, 1e-5)
	assert.Equal(t, "inverse", hits[2].Pattern.Labels["job"])
	hits, err = s.SearchPatterns(ctx, model.PatternSearch{Reference: patterns[0], Labels: map[string]string{"job": "inverse"}, Limit: 3})
	require.NoError(t, err)
	require.Len(t, hits, 1)
	require.NoError(t, s.DB.Close())
	s, err = store.Open(path)
	require.NoError(t, err)
	defer s.DB.Close()
	p, err := s.Pattern(ctx, patterns[0].ID)
	require.NoError(t, err)
	assert.Equal(t, patterns[0].Values, p.Values)
	bad := patterns[0]
	bad.Values = nil
	require.Error(t, s.SavePatterns(ctx, []model.Pattern{bad}))
	list, err = s.Patterns(ctx, 100)
	require.NoError(t, err)
	assert.Len(t, list, 4)
	require.NoError(t, s.DeletePattern(ctx, p.ID))
	require.Error(t, s.DeletePattern(ctx, p.ID))
}
