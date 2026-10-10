package store_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDashboardRevisionSerializesConcurrentEditsAndSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	s, err := store.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.DB.Close() })
	ctx := context.Background()
	baseline, err := s.Dashboards(ctx)
	require.NoError(t, err)
	initial, err := s.SaveDashboard(ctx, model.Dashboard{ID: "concurrent", Name: "Initial", Panels: []model.Panel{}})
	require.NoError(t, err)
	const writers = 16
	results := make(chan error, writers)
	var group sync.WaitGroup
	for i := range writers {
		group.Go(func() {
			draft := initial
			draft.Name = fmt.Sprintf("Writer %d", i)
			_, err := s.SaveDashboardAtRevision(ctx, draft, initial.UpdatedAt)
			results <- err
		})
	}
	group.Wait()
	close(results)
	won := 0
	for err := range results {
		if err == nil {
			won++
		} else {
			assert.ErrorIs(t, err, store.ErrDashboardConflict)
		}
	}
	require.Equal(t, 1, won)
	require.NoError(t, s.DB.Close())
	s, err = store.Open(path)
	require.NoError(t, err)
	list, err := s.Dashboards(ctx)
	require.NoError(t, err)
	var saved model.Dashboard
	for _, item := range list {
		if item.ID == initial.ID {
			saved = item
		}
	}
	require.Equal(t, initial.ID, saved.ID)
	assert.Greater(t, saved.UpdatedAt, initial.UpdatedAt)
	assert.Contains(t, saved.Name, "Writer ")
	_, err = s.SaveDashboardAtRevision(ctx, initial, initial.UpdatedAt)
	require.ErrorIs(t, err, store.ErrDashboardConflict)
	require.NoError(t, s.DeleteDashboard(ctx, initial.ID))
	_, err = s.SaveDashboardAtRevision(ctx, saved, saved.UpdatedAt)
	require.ErrorIs(t, err, store.ErrDashboardConflict)
	list, err = s.Dashboards(ctx)
	require.NoError(t, err)
	assert.Equal(t, baseline, list)
}
