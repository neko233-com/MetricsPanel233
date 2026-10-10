package alerting

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/promcompat"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGroupMutationGatesEveryAffectedGroupAndReleasesOnCancellation(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "gates.db"))
	require.NoError(t, err)
	defer s.DB.Close()
	e := New(s, promcompat.New(s))
	rules := []model.AlertRule{{UID: "a", Title: "A", Expr: "vector(1)"}, {UID: "b", Title: "B", Expr: "vector(1)"}}
	_, err = e.ReplaceRuleGroup(ctx, "general", "source", 30, rules, "api")
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
	go func() { _, err := e.Evaluate(queryCtx, "b", time.Now()); done <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("evaluation did not enter")
	}
	// Moving A would reindex B, so B's evaluation gates the whole move.
	_, err = e.ReplaceRuleGroup(ctx, "general", "dest", 30, rules[:1], "api")
	require.ErrorIs(t, err, ErrBusy)
	_, err = e.ReplaceRuleGroup(ctx, "general", "source", 30, []model.AlertRule{}, "api")
	require.ErrorIs(t, err, ErrBusy)
	_, err = e.ReplaceRuleGroup(ctx, "general", "unrelated", 30, []model.AlertRule{{UID: "c", Title: "C", Expr: "vector(1)"}}, "api")
	require.NoError(t, err, "unrelated evaluation must not block another group")
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("evaluation did not stop")
	}
	_, err = e.ReplaceRuleGroup(ctx, "general", "dest", 30, rules[:1], "api")
	require.NoError(t, err)
	b, err := s.AlertRule(ctx, "b")
	require.NoError(t, err)
	assert.Zero(t, b.GroupIndex)
	_, err = e.ReplaceRuleGroup(ctx, "general", "dest", 30, []model.AlertRule{{UID: "invalid", Title: "Bad", Expr: "rate("}}, "api")
	require.Error(t, err)
	_, err = e.ReplaceRuleGroup(ctx, "general", "dest", 30, []model.AlertRule{}, "api")
	require.NoError(t, err, "failed transaction must release its UID gates")
}
