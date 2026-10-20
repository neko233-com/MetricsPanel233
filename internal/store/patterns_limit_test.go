package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/stretchr/testify/require"
)

func TestSQLitePatternScanStopsAtTheConfiguredLimit(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "patterns.db"))
	require.NoError(t, err)
	defer s.DB.Close()
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		p := model.Pattern{Metric: "orders", Start: 1000, End: 2000, Normalization: "shape", Aggregation: "last", Coverage: 1, Values: make([]float32, model.PatternDimensions)}
		for j := range p.Values {
			p.Values[j] = float32(i*64 + j)
		}
		p.SetID()
		payload, err := json.Marshal(p)
		require.NoError(t, err)
		_, err = s.DB.ExecContext(ctx, `INSERT INTO patterns(id,metric,labels,normalization,aggregation,created_at,payload) VALUES(?,?,?,?,?,?,?)`, p.ID, p.Metric, "{}", p.Normalization, p.Aggregation, p.CreatedAt, string(payload))
		require.NoError(t, err)
	}
	reference := model.Pattern{Metric: "orders", Start: 1000, End: 2000, Normalization: "shape", Aggregation: "last", Coverage: 1, Values: make([]float32, model.PatternDimensions)}
	previous := sqlitePatternScanLimit
	sqlitePatternScanLimit = 3
	t.Cleanup(func() { sqlitePatternScanLimit = previous })
	hits, err := s.SearchPatterns(ctx, model.PatternSearch{Reference: reference, Limit: 2, IncludeSelf: true})
	require.NoError(t, err)
	require.Len(t, hits, 2)

	sqlitePatternScanLimit = 2
	_, err = s.SearchPatterns(ctx, model.PatternSearch{Reference: reference, Limit: 2, IncludeSelf: true})
	require.ErrorContains(t, err, "SQLite vector scan exceeds 2 patterns")
}
