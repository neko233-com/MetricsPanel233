package grafana

import (
	"bytes"
	"encoding/json"
	"errors"
)

// Older Grafana snapshots store DataFrameDTO fields with inline values.
// Normalize only the runtime panel; keep the original resource exact for export.
func legacySnapshotFrames(raw json.RawMessage) ([]map[string]any, error) {
	var source []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &source); err != nil {
		return nil, errors.New("snapshotData must be an array of frames")
	}
	if len(source) > 128 {
		return nil, errors.New("snapshotData exceeds 128 frames")
	}
	frames := make([]map[string]any, 0, len(source))
	for _, frame := range source {
		if frame == nil {
			return nil, errors.New("snapshotData frames must be objects")
		}
		var fields []map[string]json.RawMessage
		if err := json.Unmarshal(frame["fields"], &fields); err != nil || len(fields) > 1024 || bytes.Equal(bytes.TrimSpace(frame["fields"]), []byte("null")) {
			return nil, errors.New("snapshotData requires at most 1024 fields")
		}
		schemas := make([]map[string]json.RawMessage, 0, len(fields))
		columns := make([]json.RawMessage, 0, len(fields))
		rows := -1
		for _, field := range fields {
			var values []json.RawMessage
			if field == nil || json.Unmarshal(field["values"], &values) != nil || len(values) > 10000 || bytes.Equal(bytes.TrimSpace(field["values"]), []byte("null")) {
				return nil, errors.New("snapshotData requires arrays of at most 10000 field values")
			}
			if rows >= 0 && rows != len(values) {
				return nil, errors.New("snapshotData fields must have equal lengths")
			}
			rows = len(values)
			columns = append(columns, field["values"])
			delete(field, "values")
			schemas = append(schemas, field)
		}
		schema := map[string]any{"fields": schemas}
		for _, key := range []string{"refId", "name", "meta"} {
			if value, ok := frame[key]; ok {
				schema[key] = value
			}
		}
		frames = append(frames, map[string]any{"schema": schema, "data": map[string]any{"values": columns}})
	}
	return frames, nil
}
