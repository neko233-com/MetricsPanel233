package main

func addMonitoringWorkflowsSchema(result map[string]any, routes map[string][]string) {
	result["commands"] = append(result["commands"].([]string),
		"dashboards layout --id ID --revision UPDATED_AT --file FILE|- [--message TEXT]",
		"dashboards queries --id ID --revision UPDATED_AT --file FILE|- [--message TEXT]",
		"dashboards panels --id ID --revision UPDATED_AT --file FILE|- [--message TEXT]",
		"dashboards annotation-queries --id ID --revision UPDATED_AT --file FILE|- [--message TEXT]",
		"dashboards variables --id ID --revision UPDATED_AT --file FILE|- [--message TEXT]",
		"mute-timings list|get|save|delete|preview [--id NAME --file FILE|-]",
		"notifications groups",
		"notification-templates list|get|save|delete|preview [--id NAME --file FILE|-] (updates require version in JSON)",
		"inhibition-rules list|get|save|delete|preview|active [--id UID --file FILE|- --version N --limit 10] (updates require version; deletion requires --version)",
		"silences list [--state active|pending|expired --filter MATCHER]",
		"silences get|expire --id ID; silences save --file FILE|- [--id ID]; silences preview --file FILE|-",
		"contacts list|get|save|test|delete [--id UID --file FILE|-] (updates require version in JSON)",
		"notifications list [--limit 1000]", "notifications retry --id DELIVERY_ID", "notifications policy", "notifications policy-save --file FILE|-")
	routes["PATCH"] = append(routes["PATCH"], "/api/v1/dashboards/{id}/layout", "/api/v1/dashboards/{id}/queries", "/api/v1/dashboards/{id}/panels", "/api/v1/dashboards/{id}/annotations")
	routes["PATCH"] = append(routes["PATCH"], "/api/v1/dashboards/{id}/variables")
	for _, base := range []string{"/api/v1/alerting/mute-timings", "/api/v1/provisioning/mute-timings"} {
		routes["GET"] = append(routes["GET"], base, base+"/{name}")
		routes["POST"] = append(routes["POST"], base)
		routes["PUT"] = append(routes["PUT"], base+"/{name}")
		routes["DELETE"] = append(routes["DELETE"], base+"/{name}")
	}
	routes["GET"] = append(routes["GET"], "/api/v1/alerting/notification-groups")
	for _, base := range []string{"/api/v1/alerting/inhibition-rules", "/apis/notifications.alerting.grafana.app/v1beta1/namespaces/{namespace}/inhibitionrules"} {
		routes["GET"] = append(routes["GET"], base, base+"/{uid}")
		routes["POST"] = append(routes["POST"], base)
		routes["PUT"] = append(routes["PUT"], base+"/{uid}")
		routes["DELETE"] = append(routes["DELETE"], base+"/{uid}")
	}
	routes["GET"] = append(routes["GET"], "/api/v1/alerting/inhibition-rules/active")
	routes["POST"] = append(routes["POST"], "/api/v1/alerting/inhibition-rules/preview")
	for _, base := range []string{"/api/v1/alerting/notification-templates", "/api/v1/provisioning/templates"} {
		routes["GET"] = append(routes["GET"], base, base+"/{name}")
		routes["PUT"] = append(routes["PUT"], base+"/{name}")
		routes["DELETE"] = append(routes["DELETE"], base+"/{name}")
	}
	routes["POST"] = append(routes["POST"], "/api/v1/alerting/notification-templates", "/api/v1/alerting/notification-templates/preview")
	routes["POST"] = append(routes["POST"], "/api/v1/alerting/mute-timings/{name}/preview", "/api/v1/alerting/mute-timings/preview")
	routes["GET"] = append(routes["GET"], "/api/v1/alerting/silences", "/api/v1/alerting/silences/{id}", "/api/alertmanager/grafana/api/v2/silences", "/api/alertmanager/grafana/api/v2/silence/{id}")
	routes["POST"] = append(routes["POST"], "/api/v1/alerting/silences", "/api/v1/alerting/silences/preview", "/api/alertmanager/grafana/api/v2/silences")
	routes["DELETE"] = append(routes["DELETE"], "/api/v1/alerting/silences/{id}", "/api/alertmanager/grafana/api/v2/silence/{id}")
	routes["GET"] = append(routes["GET"], "/api/v1/alerting/contact-points", "/api/v1/alerting/contact-points/{uid}", "/api/v1/alerting/notification-policy", "/api/v1/alerting/notifications", "/api/v1/provisioning/contact-points", "/api/v1/provisioning/policies")
	routes["POST"] = append(routes["POST"], "/api/v1/alerting/contact-points", "/api/v1/alerting/contact-points/{uid}/test", "/api/v1/alerting/notifications/{id}/retry", "/api/v1/provisioning/contact-points")
	routes["PUT"] = append(routes["PUT"], "/api/v1/alerting/contact-points/{uid}", "/api/v1/alerting/notification-policy", "/api/v1/provisioning/contact-points/{uid}", "/api/v1/provisioning/policies")
	routes["DELETE"] = append(routes["DELETE"], "/api/v1/alerting/contact-points/{uid}", "/api/v1/provisioning/contact-points/{uid}", "/api/v1/provisioning/policies")
	result["dashboard_layout"] = map[string]any{
		"grid":        "24 columns; x 0–23, y 0–10000, w 1–24 within grid, h 1–100",
		"body":        "{panels:[{id:NATIVE_PANEL_ID,x:0,y:0,w:12,h:8}]}; every base panel exactly once",
		"concurrency": "If-Match=updated_at is required (428 when omitted); stale edits fail with 409; layout creates a version history entry",
		"source":      "server patches stored raw JSON; native, Classic, V1, V2 Grid/AutoGrid/Rows/Tabs; AutoGrid becomes explicit Grid; row/tab membership retained and cross-section moves rejected",
		"desktop":     "mouse drag/resize, numeric coordinates and arrow keys; Shift+arrows resizes; cancel does not save",
	}
	result["alert_notifications"] = map[string]any{
		"contact_points":  "webhook POST over HTTP/HTTPS; encrypted write-only bearer_token and headers; omitted secrets preserve values; versioned updates",
		"routing":         "recursive ordered label matcher routes (=, !=, =~, !~); inherited receiver/grouping; continue controls multiple receivers; up to 8 nested levels and 128 routes",
		"per_rule":        "optional native notification_receiver overrides policy; Grafana notification_settings supports receiver only",
		"delivery":        "durable firing/resolved/test outbox, bounded HTTP timeout, retries and attempt/status records; dispatcher resumes after restart",
		"compatibility":   "Grafana provisioning contact point and policy APIs accept supported webhook settings; unsupported integration options rejected explicitly",
		"silences":        "persistent active/pending/expired silences, equality/regex/negative matchers, preview and expire; suppression survives restart; expiry resumes only still-firing alerts",
		"grouping":        "persistent group_by/group_wait/group_interval/repeat_interval; initial wait, updated members, resolved members and repeated notifications; dispatcher restores groups and delivery claims after restart",
		"mute_timings":    "named timezone-aware Alertmanager time intervals; weekdays/months/days/years and time ranges; per-route mute_time_intervals/active_time_intervals are not inherited; preview and versioned CRUD",
		"templates":       "versioned named Go notification template groups, Alertmanager functions and common Grafana-shaped alert data; read-only draft preview, contact title/message selection and frozen queued payloads across retries/restarts",
		"template_limits": "128 KiB rendered output and bounded execution; Grafana namespaced functions, full per-RefID graph Values and exact Grafana default formatting remain pending",
		"inhibition":      "durable source/target matchers and equal labels, indexed active instances, preview and versioned CRUD; suppression composes with silences and calendars, current labels update without state changes, only still-firing targets resume after source removal",
		"pending":         []string{"email, Slack and other native integrations", "remaining Alertmanager APIs"},
	}
	result["alert_inhibition"] = map[string]any{
		"native":   "inhibition-rules save --file FILE|- creates a generated UID; updates use --id UID and current version in JSON or --version N; deletion requires --id UID --version N; stale writes fail with 409",
		"matchers": "source_matchers and target_matchers each have 1 to 32 {name,operator,value} matchers; operators =,!=,=~,!~; equal has at most 32 unique label names; missing and empty equal labels match",
		"preview":  "read-only POST /preview accepts source_matchers,target_matchers,equal,disabled,optional labels and limit; omitted labels inspect active targets, labels:{} previews an empty-label candidate; source/target counts are exact and visible results bounded",
		"active":   "inhibition-rules active --limit 1..200 lists real persisted active instances with total and truncation; Pending instances excluded; firing, NoData, Error and Recovering retained",
		"grafana":  "notifications.alerting.grafana.app/v1beta1 inhibitionrules CRUD, namespace-scoped spec source_matchers/target_matchers {label,type,value} and equal; metadata.name is immutable UID; resourceVersion is the pinned sorted-content FNV64 fingerprint",
		"scope":    "local Grafana-managed alerts; imported external Alertmanager configurations, full AppPlatform metadata/watch and file provisioning remain pending",
	}
	result["dashboard_query_edits"] = map[string]any{
		"body":        "{panels:[{id:NATIVE_PANEL_ID,queries:[{originalRef:OLD_REF,baseline:ORIGINAL_BROWSER_QUERY,query:EDITED_QUERY}]}]}; new queries omit originalRef/baseline; removed queries omitted",
		"concurrency": "mandatory If-Match=updated_at; atomic multi-panel commit, version history and stale edit rejection",
		"precision":   "server merges only changed query fields into stored raw models; unchanged large numbers, other panels and Classic/V1/V2 envelopes retained even after browser JSON rounding",
		"libraries":   "linked panel queries are edited through the shared library model",
	}
	result["native_panel_edits"] = map[string]any{
		"body":    "{action:save|duplicate|delete,sourceId:EXISTING_ID,panel:{id,title,visualization,unit,metric,expr,aggregation,labels},position:{x,y,w,h}}; new and duplicated panels require a position; deletion requires sourceId",
		"save":    "native dashboards only; mandatory If-Match, version history; server retains unchanged raw query/plugin settings and other panels; duplicates receive unique numeric Grafana panel IDs",
		"queries": "Panel queries is available on native and imported dashboards; native dashboards render configured SQL, Prometheus, expressions and installed SDK sources",
	}
	result["annotation_query_edits"] = map[string]any{
		"body":        "{queries:[{originalIndex:0,baseline:RAW_BROWSER_ANNOTATION,query:EDITED_RAW_ANNOTATION}]}; new queries omit originalIndex/baseline; omitted queries are deleted; empty array removes all; 32 queries and 128 KiB maximum",
		"concurrency": "mandatory If-Match=updated_at; atomic revision/history commit; unchanged raw fields survive browser number rounding in native, Classic, V1 and V2 resource envelopes",
	}
	result["dashboard_variable_edits"] = map[string]any{
		"body": "{variables:[{originalName:NAME,baseline:ORIGINAL_BROWSER_NATIVE_VARIABLE,variable:EDITED_NATIVE_VARIABLE}]}; complete ordered desired array, new variables omit originalName/baseline, omitted variables deleted",
		"save": "mandatory If-Match; 32 unique variables; native/Classic/V1/V2 raw merging preserves unchanged plugin fields, numbers, envelopes and panels; version history is atomic",
	}
	result["sql_datasources"] = map[string]any{
		"types":         []string{"mysql (MySQL/MariaDB)", "grafana-postgresql-datasource (PostgreSQL; postgres alias accepted)"},
		"configuration": "host:port, database, user; encrypted secureJsonData.password and inline TLS certificates; bounded read-only query transactions and persistent connection pools",
		"query":         "datasources query --file FILE|- uses standard /api/ds/query rawSql,format,refId,datasource.uid; table/time_series frames, nulls, time macros, fill, legacy metric columns, JSONL and expression graph queries",
		"variables":     "configured SQL metricFindQuery and __text/__value options through Grafana SDK",
	}
	graphs := result["alert_graphs"].(map[string]any)
	graphs["sources"] = "configured Prometheus, builtin MySQL/MariaDB/PostgreSQL SQL, builtin Grafana and installed backend SDK datasources"
	graphs["pending"] = []string{"remaining notification template functions and receiver integrations", "remaining Alertmanager APIs", "remaining Grafana core datasource editors"}
	queries := result["dashboard_queries"].(map[string]any)
	queries["editor"] = "Panel queries on native and imported dashboards; installed SDK QueryEditor, onChange/onRunQuery/onAddQuery, shared plugin context, range and per-RefID response data"
	queries["concurrency"] = "PATCH /api/v1/dashboards/{id}/queries requires If-Match=updated_at; atomic query/history commit, raw precision retention and stale edit HTTP 409"
	queries["agent"] = "dashboards queries --id ID --revision UPDATED_AT --file FILE|-; see dashboard_query_edits for the baseline merge contract"
}
