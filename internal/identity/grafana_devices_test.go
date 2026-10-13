package identity

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGrafanaDevicePinnedRealUserAgentFixtures(t *testing.T) {
	const edge = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.6723.69 Safari/537.36 Edg/130.0.2849.68"
	session := Session{ID: 9007199254740993, ClientIP: "192.0.2.1", CreatedAt: 1760000000123, UserAgent: edge, AuthModule: "password"}
	dto, err := grafanaDeviceDTO(session, session.ID)
	require.NoError(t, err)
	require.Equal(t, "Edge", dto.Browser)
	require.Equal(t, "130.0", dto.BrowserVersion)
	require.Equal(t, "Windows", dto.OS)
	require.Equal(t, "10", dto.OSVersion)
	require.Equal(t, "Other", dto.Device)
	require.True(t, dto.IsActive)
	require.Empty(t, dto.AuthModule)
	require.Equal(t, dto.CreatedAt, dto.SeenAt)
	require.Equal(t, "2025-10-09T08:53:20.123Z", dto.CreatedAt.Format(time.RFC3339Nano))
	require.Equal(t, edge, session.UserAgent)
	require.Equal(t, "password", session.AuthModule)
	var wg sync.WaitGroup
	results := make(chan GrafanaAuthToken, 12)
	failures := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			item, err := grafanaDeviceDTO(session, session.ID)
			results <- item
			failures <- err
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	for item := range results {
		require.Equal(t, dto, item)
	}
	unknown, err := grafanaDeviceDTO(Session{CreatedAt: session.CreatedAt}, 0)
	require.NoError(t, err)
	require.Equal(t, "Other", unknown.Browser)
	require.Equal(t, "Other", unknown.OS)
	require.Equal(t, "Other", unknown.Device)
	require.Empty(t, unknown.BrowserVersion)
	require.False(t, unknown.IsActive)
}

func TestGrafanaDeviceListCancellationAndEmptyArray(t *testing.T) {
	s, _ := openServiceAccountTestStore(t)
	_, p, ctx := adapterHuman(t, s, "empty")
	items, err := s.CurrentUserAuthTokens(ctx, p, 0)
	require.NoError(t, err)
	require.NotNil(t, items)
	require.Empty(t, items)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = s.CurrentUserAuthTokens(canceled, p, 0)
	require.ErrorIs(t, err, context.Canceled)
}
