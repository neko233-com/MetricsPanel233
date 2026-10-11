package alerting

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/promcompat"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/require"
)

func TestFolderDeletionGatesActiveRecordingRules(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "folder.db"))
	require.NoError(t, err)
	defer s.DB.Close()
	e := New(s, promcompat.New(s))
	_, err = s.CreateFolder(ctx, model.Folder{UID: "team", Title: "Team"})
	require.NoError(t, err)
	_, err = e.SaveRule(ctx, model.AlertRule{UID: "recording", Title: "Recording", Expr: "vector(1)", Record: "recorded_folder_value", FolderUID: "team"})
	require.NoError(t, err)
	entered := make(chan struct{})
	e.Query = func(ctx context.Context, _ string, _ time.Time) ([]Value, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	queryCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := e.Evaluate(queryCtx, "recording", time.Now()); done <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("rule did not enter evaluation")
	}
	require.ErrorIs(t, e.DeleteFolder(ctx, "team", true), ErrBusy)
	_, err = s.Folder(ctx, "team")
	require.NoError(t, err)
	_, err = s.AlertRule(ctx, "recording")
	require.NoError(t, err)
	_, err = s.CreateFolder(ctx, model.Folder{UID: "other", Title: "Other"})
	require.NoError(t, err)
	require.NoError(t, e.DeleteFolder(ctx, "other", true), "unrelated folder must not wait for active evaluation")
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	require.NoError(t, e.DeleteFolder(ctx, "team", true))
	require.Empty(t, e.active)
}
