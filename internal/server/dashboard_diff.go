package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"math/big"
	"sort"
	"strconv"
	"strings"

	"github.com/neko233-com/MetricsPanel233/internal/model"
)

type dashboardChange struct {
	Path      string          `json:"path"`
	Operation string          `json:"operation"`
	Before    json.RawMessage `json:"before,omitempty"`
	After     json.RawMessage `json:"after,omitempty"`
}

// Compare JSON number values without converting opaque fields to float64.
func normalizedJSONNumber(value json.Number) string {
	s := string(value)
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}
	exponent := new(big.Int)
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		// Avoid expensive conversion of a deliberately enormous exponent token.
		if len(s[i+1:]) > 2048 {
			return string(value)
		}
		if _, ok := exponent.SetString(s[i+1:], 10); !ok {
			return string(value)
		}
		s = s[:i]
	}
	if i := strings.IndexByte(s, '.'); i >= 0 {
		exponent.Sub(exponent, big.NewInt(int64(len(s)-i-1)))
		s = s[:i] + s[i+1:]
	}
	s = strings.TrimLeft(s, "0")
	if s == "" {
		return "0"
	}
	trimmed := strings.TrimRight(s, "0")
	exponent.Add(exponent, big.NewInt(int64(len(s)-len(trimmed))))
	return sign + trimmed + "e" + exponent.String()
}

func historyJSON(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func historySpec(d model.Dashboard) (any, error) {
	if len(d.Grafana) > 0 {
		doc := dashboardInputObject(d.Grafana)
		if doc == nil {
			return nil, errors.New("historical dashboard has an invalid source")
		}
		// Identity/version and resource metadata belong to the current dashboard.
		delete(doc, "id")
		delete(doc, "uid")
		delete(doc, "version")
		doc["title"] = d.Name
		return doc, nil
	}
	return historyJSON(rawValue(map[string]any{"title": d.Name, "panels": d.Panels, "variables": d.Variables, "annotations": d.Annotations}))
}

func diffDashboardJSON(base, next any) []dashboardChange {
	changes := []dashboardChange{}
	var walk func(string, any, any)
	walk = func(path string, before, after any) {
		if a, ok := before.(map[string]any); ok {
			if b, ok := after.(map[string]any); ok {
				keys := make(map[string]bool, len(a)+len(b))
				for key := range a {
					keys[key] = true
				}
				for key := range b {
					keys[key] = true
				}
				ordered := make([]string, 0, len(keys))
				for key := range keys {
					ordered = append(ordered, key)
				}
				sort.Strings(ordered)
				for _, key := range ordered {
					x, hasBefore := a[key]
					y, hasAfter := b[key]
					child := path + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
					switch {
					case !hasBefore:
						changes = append(changes, dashboardChange{Path: child, Operation: "add", After: rawValue(y)})
					case !hasAfter:
						changes = append(changes, dashboardChange{Path: child, Operation: "remove", Before: rawValue(x)})
					default:
						walk(child, x, y)
					}
				}
				return
			}
		}
		if a, ok := before.([]any); ok {
			if b, ok := after.([]any); ok {
				length := len(a)
				if len(b) > length {
					length = len(b)
				}
				for i := 0; i < length; i++ {
					child := path + "/" + strconv.Itoa(i)
					switch {
					case i >= len(a):
						changes = append(changes, dashboardChange{Path: child, Operation: "add", After: rawValue(b[i])})
					case i >= len(b):
						changes = append(changes, dashboardChange{Path: child, Operation: "remove", Before: rawValue(a[i])})
					default:
						walk(child, a[i], b[i])
					}
				}
				return
			}
		}
		if a, ok := before.(json.Number); ok {
			if b, ok := after.(json.Number); ok && normalizedJSONNumber(a) == normalizedJSONNumber(b) {
				return
			}
		} else if bytes.Equal(rawValue(before), rawValue(after)) {
			return
		}
		changes = append(changes, dashboardChange{Path: path, Operation: "replace", Before: rawValue(before), After: rawValue(after)})
	}
	walk("", base, next)
	return changes
}

func compareDashboardSpecs(base, next model.Dashboard) ([]dashboardChange, error) {
	a, err := historySpec(base)
	if err != nil {
		return nil, err
	}
	b, err := historySpec(next)
	if err != nil {
		return nil, err
	}
	return diffDashboardJSON(a, b), nil
}
