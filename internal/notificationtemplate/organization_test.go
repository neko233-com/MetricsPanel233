package notificationtemplate

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestDataOrganizationPropagatesOwnerAndKeepsLegacyDefault(t *testing.T) {
	at := time.Unix(1000, 0)
	payload := map[string]any{"orgId": 2, "receiver": "Ops", "status": "firing", "externalURL": "https://metrics.example/?orgId=1", "alerts": []any{map[string]any{"status": "firing", "labels": map[string]string{"alertname": "Example"}, "annotations": map[string]string{"__orgId__": "1", "__dashboardUid__": "dashboard", "__panelId__": "2"}, "startsAt": at.UTC().Format(time.RFC3339Nano)}}}
	data, err := DataFromPayload(payload, at)
	require.NoError(t, err)
	assert.Equal(t, int64(2), data.OrgID)
	require.NotNil(t, data.Alerts[0].OrgID)
	assert.Equal(t, int64(2), *data.Alerts[0].OrgID)
	assert.Contains(t, data.Alerts[0].DashboardURL, "orgId=2")
	assert.Contains(t, data.Alerts[0].PanelURL, "orgId=2")
	assert.Contains(t, data.Alerts[0].SilenceURL, "orgId=2")
	assert.NotContains(t, data.Alerts[0].SilenceURL, "orgId=1")
	delete(payload, "orgId")
	data, err = DataFromPayload(payload, at)
	require.NoError(t, err)
	assert.Equal(t, int64(1), *data.Alerts[0].OrgID)
}
