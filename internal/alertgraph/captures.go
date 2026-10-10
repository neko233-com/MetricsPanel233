package alertgraph

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/neko233-com/MetricsPanel233/internal/alerttemplates"
	"github.com/neko233-com/MetricsPanel233/internal/expressions"
)

type captureGroup struct {
	keys        []string
	items       map[string]alerttemplates.Capture
	projections map[string]map[string][]alerttemplates.Capture
}
type captureIndex struct {
	refs   []string
	groups map[string][]*captureGroup
	exact  map[string]map[string]alerttemplates.Capture
	ctx    context.Context
	work   int
}

func sortedKeys(ls map[string]string) []string { return slices.Sorted(maps.Keys(ls)) }
func projection(ls map[string]string, keys []string) (string, bool) {
	values := make([]string, 0, len(keys)*2)
	for _, key := range keys {
		value, ok := ls[key]
		if !ok {
			return "", false
		}
		values = append(values, key, value)
	}
	raw, _ := json.Marshal(values)
	return string(raw), true
}
func (i *captureIndex) step() error {
	i.work++
	if i.work > 2000000 {
		return fmt.Errorf("alert template captures exceed computation limit")
	}
	return i.ctx.Err()
}
func newCaptureIndex(ctx context.Context, p *Plan, responses backend.Responses) (*captureIndex, error) {
	i := &captureIndex{ctx: ctx, groups: map[string][]*captureGroup{}, exact: map[string]map[string]alerttemplates.Capture{}}
	for _, q := range p.Data {
		groups := map[string]*captureGroup{}
		i.exact[q.RefID] = map[string]alerttemplates.Capture{}
		kind := "query"
		if expressions.IsSource(q.DatasourceUID) {
			kind, _, _ = expressions.Describe(q.Model)
		}
		for _, frame := range responses[q.RefID].Frames {
			if err := i.step(); err != nil {
				return nil, err
			}
			if len(frame.Fields) != 1 || !frame.Fields[0].Type().Numeric() {
				continue
			}
			field := frame.Fields[0]
			var number *float64
			if field.Len() == 1 {
				var err error
				number, err = field.NullableFloatAt(0)
				if err != nil {
					return nil, err
				}
				if number != nil {
					copy := *number
					number = &copy
				}
			}
			capture := alerttemplates.Capture{Labels: maps.Clone(field.Labels), Value: number, Datasource: !expressions.IsSource(q.DatasourceUID), Type: kind}
			keys := sortedKeys(capture.Labels)
			setJSON, _ := json.Marshal(keys)
			set := string(setJSON)
			group := groups[set]
			if group == nil {
				group = &captureGroup{keys: keys, items: map[string]alerttemplates.Capture{}, projections: map[string]map[string][]alerttemplates.Capture{}}
				groups[set] = group
			}
			fingerprint, _ := projection(capture.Labels, keys)
			group.items[fingerprint] = capture
			i.exact[q.RefID][fingerprint] = capture
		}
		for _, group := range groups {
			i.groups[q.RefID] = append(i.groups[q.RefID], group)
		}
		i.refs = append(i.refs, q.RefID)
	}
	slices.Sort(i.refs)
	return i, nil
}
func (i *captureIndex) match(labels map[string]string) (map[string]alerttemplates.Capture, string, error) {
	keys := sortedKeys(labels)
	full, _ := projection(labels, keys)
	keyJSON, _ := json.Marshal(keys)
	values := map[string]alerttemplates.Capture{}
	var text []string
	for _, ref := range i.refs {
		if err := i.step(); err != nil {
			return nil, "", err
		}
		matches := []alerttemplates.Capture{}
		if exact, ok := i.exact[ref][full]; ok {
			matches = append(matches, exact)
		} else {
			for _, group := range i.groups[ref] {
				if err := i.step(); err != nil {
					return nil, "", err
				}
				if len(group.keys) < len(keys) {
					if subset, ok := projection(labels, group.keys); ok {
						if capture, found := group.items[subset]; found {
							matches = append(matches, capture)
						}
					}
					continue
				}
				if len(group.keys) == len(keys) {
					continue
				}
				index, exists := group.projections[string(keyJSON)]
				if !exists {
					index = map[string][]alerttemplates.Capture{}
					for _, capture := range group.items {
						if err := i.step(); err != nil {
							return nil, "", err
						}
						if subset, ok := projection(capture.Labels, keys); ok {
							index[subset] = append(index[subset], capture)
						}
					}
					if len(group.projections) < 8 {
						group.projections[string(keyJSON)] = index
					}
				}
				matches = append(matches, index[full]...)
			}
		}
		slices.SortFunc(matches, func(a, b alerttemplates.Capture) int {
			return strings.Compare(alerttemplates.Labels(a.Labels).String(), alerttemplates.Labels(b.Labels).String())
		})
		for _, capture := range matches {
			values[ref] = capture
			text = append(text, alerttemplates.Evaluation(ref, capture))
		}
	}
	return values, strings.Join(text, ", "), nil
}

func classicCaptures(ref string, raw json.RawMessage) (map[string]alerttemplates.Capture, string, error) {
	var matches []struct {
		Value  *string           `json:"value"`
		Metric string            `json:"metric"`
		Labels map[string]string `json:"labels"`
	}
	if err := json.Unmarshal(raw, &matches); err != nil {
		return nil, "", err
	}
	values := map[string]alerttemplates.Capture{}
	text := make([]string, 0, len(matches))
	for index, match := range matches {
		var number *float64
		if match.Value != nil && *match.Value != "" && *match.Value != "null" {
			n, err := strconv.ParseFloat(*match.Value, 64)
			if err != nil {
				return nil, "", err
			}
			number = &n
		}
		key := ref + strconv.Itoa(index)
		capture := alerttemplates.Capture{Labels: maps.Clone(match.Labels), Value: number, Type: "classic_conditions"}
		values[key] = capture
		value := "null"
		if number != nil {
			value = fmt.Sprint(*number)
		}
		text = append(text, fmt.Sprintf("[ var='%s' metric='%s' labels={%s} type='classic_conditions' value=%s ]", key, match.Metric, alerttemplates.Labels(match.Labels), value))
	}
	return values, strings.Join(text, ", "), nil
}
