package server_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNotificationRecursiveProvisioningTimeIntervalsRoundtrip(t *testing.T) {
	_, h := setup(t, "")
	w := call(h, "POST", "/api/v1/alerting/contact-points", `{"uid":"ops","name":"Operations","url":"http://localhost/webhook"}`, "", "")
	require.Equal(t, 201, w.Code, w.Body.String())
	timing := `{"name":"month-end","time_intervals":[{"times":[{"start_time":"22:00","end_time":"24:00"}],"weekdays":["monday:friday"],"days_of_month":["-3:-1"],"months":["january:december"],"years":["2026:2030"],"location":"America/New_York"}],"provenance":"file","version":""}`
	w = call(h, "POST", "/api/v1/provisioning/mute-timings", timing, "", "")
	require.Equal(t, 201, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"version":"1"`)
	assert.Contains(t, w.Body.String(), `"days_of_month":["-3:-1"]`)
	w = call(h, "GET", "/api/v1/provisioning/mute-timings/month-end", "", "", "")
	require.Equal(t, 200, w.Code)
	old := w.Body.String()
	w = call(h, "PUT", "/api/v1/provisioning/mute-timings/month-end", old, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"version":"2"`)
	assert.Equal(t, 409, call(h, "PUT", "/api/v1/provisioning/mute-timings/month-end", old, "", "").Code)
	assert.Equal(t, 409, call(h, "DELETE", "/api/v1/provisioning/mute-timings/month-end?version=1", "", "", "").Code)
	policy := `{"receiver":"Operations","group_by":["team"],"group_wait":"1s","group_interval":"3s","repeat_interval":"5s","routes":[{"object_matchers":[["team","=","ops"]],"continue":true,"mute_time_intervals":["month-end"],"routes":[{"object_matchers":[["severity","=~","critical|warning"]],"group_by":[],"group_wait":"0s","active_time_intervals":["month-end"]}]},{"receiver":"Operations","object_matchers":[["team","=","audit"]]}]}`
	w = call(h, "PUT", "/api/v1/provisioning/policies", policy, "", "")
	require.Equal(t, 202, w.Code, w.Body.String())
	w = call(h, "GET", "/api/v1/provisioning/policies", "", "", "")
	require.Equal(t, 200, w.Code)
	exported := w.Body.String()
	var tree map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &tree))
	assert.Equal(t, "5s", tree["repeat_interval"])
	assert.Contains(t, exported, `"group_by":[]`)
	assert.Contains(t, exported, `"active_time_intervals":["month-end"]`)
	w = call(h, "PUT", "/api/v1/provisioning/policies", exported, "", "")
	require.Equal(t, 202, w.Code, w.Body.String())
	w = call(h, "GET", "/api/v1/provisioning/policies", "", "", "")
	var again map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &again))
	delete(tree, "version")
	delete(again, "version")
	assert.Equal(t, tree, again)
	assert.Equal(t, 400, call(h, "DELETE", "/api/v1/alerting/mute-timings/month-end", "", "", "").Code, "nested route references block deletion")
	assert.Equal(t, 409, call(h, "DELETE", "/api/v1/alerting/contact-points/ops", "", "", "").Code)
	assert.Equal(t, 400, call(h, "PUT", "/api/v1/alerting/notification-policy", `{"version":2,"receiver":"ops","routes":[{"mute_time_intervals":["missing"]}]}`, "", "").Code)
	assert.Equal(t, 400, call(h, "POST", "/api/v1/alerting/mute-timings", `{"name":"bad","time_intervals":[{"times":[{"start_time":"12:00","end_time":"13:00","unsupported":true}]}]}`, "", "").Code)
	w = call(h, "POST", "/api/v1/alerting/mute-timings/month-end/preview", `{"at":"2026-10-30T23:00:00-04:00"}`, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"matches":true`)
	w = call(h, "GET", "/api/v1/alerting/notification-groups", "", "", "")
	require.Equal(t, 200, w.Code)
	assert.JSONEq(t, `[]`, w.Body.String())
}

func TestNotificationProvisioningDefaultsAndLegacyRoundtrip(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "new-grafana-defaults", true: "legacy-native-immediate"}[legacy], func(t *testing.T) {
			_, h := setup(t, "")
			require.Equal(t, 201, call(h, "POST", "/api/v1/alerting/contact-points", `{"uid":"ops","name":"Operations","url":"http://localhost/webhook"}`, "", "").Code)
			if legacy {
				require.Equal(t, 200, call(h, "PUT", "/api/v1/alerting/notification-policy", `{"receiver":"ops"}`, "", "").Code)
			}
			w := call(h, "GET", "/api/v1/provisioning/policies", "", "", "")
			var p map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &p))
			p["receiver"] = "Operations"
			raw, _ := json.Marshal(p)
			w = call(h, "PUT", "/api/v1/provisioning/policies", string(raw), "", "")
			require.Equal(t, 202, w.Code, w.Body.String())
			w = call(h, "GET", "/api/v1/alerting/notification-policy", "", "", "")
			if legacy {
				assert.NotContains(t, w.Body.String(), "group_wait")
				assert.NotContains(t, w.Body.String(), "repeat_interval")
			} else {
				assert.Contains(t, w.Body.String(), `"group_wait":"30s"`)
				assert.Contains(t, w.Body.String(), `"group_interval":"5m"`)
				assert.Contains(t, w.Body.String(), `"repeat_interval":"4h"`)
			}
		})
	}
}
