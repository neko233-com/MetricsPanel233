package store_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnnotationsDurabilityIdempotencyFilteringAndAtomicPatches(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "annotations.db")
	s, err := store.Open(path)
	require.NoError(t, err)
	a := model.Annotation{Time: 1000, TimeEnd: 5000, Text: "Deployment", Tags: []string{"mysql", "deploy", "deploy"}, DashboardUID: "scope", PanelID: 2, UserID: 1, Login: "metricspanel"}
	first, err := s.CreateAnnotation(ctx, a, "deploy-233")
	require.NoError(t, err)
	repeated, err := s.CreateAnnotation(ctx, a, "deploy-233")
	require.NoError(t, err)
	assert.Equal(t, first, repeated)
	a.Text = "different"
	_, err = s.CreateAnnotation(ctx, a, "deploy-233")
	require.ErrorIs(t, err, store.ErrAnnotationConflict)
	_, err = s.CreateAnnotation(ctx, model.Annotation{Time: 9000, Text: "Global", Tags: []string{"deploy"}}, "")
	require.NoError(t, err)
	items, err := s.Annotations(ctx, model.AnnotationQuery{From: 2000, To: 3000, Tags: []string{"mysql", "deploy"}})
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, first.ID, items[0].ID)
	items, err = s.Annotations(ctx, model.AnnotationQuery{Tags: []string{"mysql", "missing"}, MatchAny: true})
	require.NoError(t, err)
	require.Len(t, items, 1)
	items, err = s.Annotations(ctx, model.AnnotationQuery{Tags: []string{"mysql", "missing"}})
	require.NoError(t, err)
	assert.Empty(t, items)
	text, tags := "Updated", []string{"release"}
	var group sync.WaitGroup
	errs := make(chan error, 2)
	for _, patch := range []model.AnnotationPatch{{Text: &text}, {Tags: &tags}} {
		group.Add(1)
		go func(p model.AnnotationPatch) {
			defer group.Done()
			_, err := s.PatchAnnotation(ctx, first.ID, p)
			errs <- err
		}(patch)
	}
	group.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.NoError(t, s.DB.Close())
	s, err = store.Open(path)
	require.NoError(t, err)
	defer s.DB.Close()
	items, err = s.Annotations(ctx, model.AnnotationQuery{ID: first.ID})
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, text, items[0].Text)
	assert.Equal(t, tags, items[0].Tags)
	assert.Equal(t, first.Created, items[0].Created)
	a.Text = "Deployment"
	replayed, err := s.CreateAnnotation(ctx, a, "deploy-233")
	require.NoError(t, err)
	assert.Equal(t, first.ID, replayed.ID)
	assert.Equal(t, "Updated", replayed.Text)
	index, err := s.AnnotationTags(ctx, "release", 100)
	require.NoError(t, err)
	require.Len(t, index, 1)
	assert.Equal(t, int64(1), index[0]["count"])
	require.NoError(t, s.DeletePanelAnnotations(ctx, "scope", 2))
	index, err = s.AnnotationTags(ctx, "release", 100)
	require.NoError(t, err)
	assert.Empty(t, index)
	items, err = s.Annotations(ctx, model.AnnotationQuery{})
	require.NoError(t, err)
	require.Len(t, items, 1)
	_, err = s.Annotations(ctx, model.AnnotationQuery{Limit: 1001})
	require.ErrorIs(t, err, model.ErrInvalidAnnotation)
}
