package main

func addHostProcessSchema(result map[string]any, routes map[string][]string) {
	routes["GET"] = append(routes["GET"], "/api/v1/host/processes", "/api/v1/host/processes/{pid}", "/api/v1/host/process-icons/{iconKey}")
	result["commands"] = append(result["commands"].([]string),
		"processes list [--prefix TEXT --limit 200 --continue-token TOKEN]",
		"processes get --pid PID [--start-token TOKEN]",
	)
	result["host_processes"] = map[string]any{
		"scope":          "read-only processes visible to the MetricsPanel233 server host/process namespace; a container server sees its container process namespace; this is independent of Prometheus scrape targets and browser/client processes",
		"source":         "source.id is a random manager/server-start identifier and changes after restart; hostname, os and scope describe the server host and visible process namespace; this is not a persistent host identity",
		"identity":       "PID alone is not durable; retain the exact string startToken from list and pass --pid plus --start-token to get; tokens are opaque and must never be parsed as JavaScript numbers; get without a token reads the current PID",
		"ordering":       "fixed case-insensitive process name ascending (A-Z), then numeric PID ascending for equal names; order=name_asc_pid_asc; no client-selected sort mode",
		"pagination":     "items plus an opaque continueToken anchored to one snapshot; pass --continue-token exactly, with prefix/limit omitted to inherit the snapshot or repeated unchanged; changing them returns HTTP 400",
		"freshness":      "sampledAt and expiresAt are Unix milliseconds; snapshotId remains fixed across pages and expires after 60 seconds; HTTP 410 requires restarting list without --continue-token; get samples the current process and optional startToken detects PID reuse",
		"icons":          "iconKey is an opaque server-issued key; GET /api/v1/host/process-icons/{iconKey} with Bearer authentication returns an image; unavailable icons use the UI fallback; no executable path is accepted",
		"authentication": "same Bearer authentication as the remaining API; METRICSPANEL_URL and METRICSPANEL_TOKEN select the remote server and credentials",
		"metrics":        "memoryRssBytes is an exact decimal string or null; CPU percent is null while warming up and later measures process CPU time over elapsed server sampling time, so multi-core processes can exceed 100%; null values and states represent unavailable, denied, exited or unsupported data",
		"operations": []map[string]any{
			{"command": "processes list", "method": "GET", "path": "/api/v1/host/processes", "flags": map[string]any{"prefix": map[string]any{"type": "string", "match": "case-insensitive process name prefix", "maxBytes": 128, "default": ""}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000, "default": 200}, "continue-token": map[string]any{"type": "string", "query": "continue", "opaque": true}}},
			{"command": "processes get", "method": "GET", "path": "/api/v1/host/processes/{pid}", "flags": map[string]any{"pid": map[string]any{"type": "integer", "minimum": 0, "maximum": uint64(4294967295), "required": true}, "start-token": map[string]any{"type": "string", "query": "startToken", "maxBytes": 256, "opaque": true}}},
		},
		"response": map[string]any{
			"list":   []string{"source", "snapshotId", "sampledAt", "expiresAt", "prefix", "order", "total", "items", "continueToken", "partial", "warnings"},
			"source": []string{"id", "hostname", "os", "scope: host|pid_namespace", "capabilities: cpu,memory,icons"},
			"item":   []string{"id", "pid", "ppid", "name", "nameSource", "startToken:string", "startedAt:number|null", "cpuSecondsTotal:number|null", "cpuPercent:number|null", "memoryRssBytes:string|null", "threads:number|null", "iconKey", "states: startTime,cpu,memory"},
			"detail": []string{"source", "sampledAt", "process"},
		},
		"errors":     map[string]string{"400": "invalid query or changed continuation parameters", "401": "valid Bearer token required", "404": "process exited or is not visible; icon key unavailable", "409": "PID now refers to a different process than startToken", "410": "snapshot expired; restart list"},
		"examples":   []string{"metricspanel processes list --prefix G --limit 10", "metricspanel processes list --continue-token CONTINUE_TOKEN", "metricspanel processes get --pid 233 --start-token START_TOKEN"},
		"boundaries": "list/get only; no termination, shell execution, command lines, environment values or executable file paths are exposed",
	}
}
