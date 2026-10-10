package alertgraph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/neko233-com/MetricsPanel233/internal/alerttemplates"
	"github.com/neko233-com/MetricsPanel233/internal/expressions"
)

type Value struct {
	Labels           map[string]string
	Value            float64
	Missing          bool
	Matches          json.RawMessage
	Captures         map[string]alerttemplates.Capture
	EvaluationString string
}

func Execute(ctx context.Context, p *Plan, at time.Time, source expressions.SourceQuery) ([]Value, error) {
	if source == nil {
		source = func(context.Context, string, []backend.DataQuery) (backend.Responses, string, error) {
			return nil, "", errors.New("alert backend datasource is not configured")
		}
	}
	response := expressions.Execute(ctx, p.Groups(at), source)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	condition, ok := response.Responses[p.Condition]
	if !ok {
		return nil, errors.New("condition result was omitted")
	}
	if condition.Error != nil {
		return nil, fmt.Errorf("condition %s: %w", p.Condition, condition.Error)
	}
	// Grafana gives datasource NoData priority over a healthy condition. Empty
	// expression results are handled by the condition's own nullable frames.
	empty := map[string][]string{}
	for _, q := range p.Data {
		if expressions.IsSource(q.DatasourceUID) {
			continue
		}
		r := response.Responses[q.RefID]
		if len(r.Frames) == 0 || (len(r.Frames) == 1 && len(r.Frames[0].Fields) == 0) {
			empty[q.DatasourceUID] = append(empty[q.DatasourceUID], q.RefID)
		}
	}
	if len(empty) > 0 {
		out := []Value{}
		uids := make([]string, 0, len(empty))
		for uid := range empty {
			uids = append(uids, uid)
		}
		slices.Sort(uids)
		for _, uid := range uids {
			refs := empty[uid]
			slices.Sort(refs)
			out = append(out, Value{Missing: true, Labels: map[string]string{"datasource_uid": uid, "ref_id": strings.Join(refs, ",")}})
		}
		return out, nil
	}
	out := []Value{}
	index, err := newCaptureIndex(ctx, p, response.Responses)
	if err != nil {
		return nil, err
	}
	classic := false
	for _, q := range p.Data {
		if q.RefID == p.Condition && expressions.IsSource(q.DatasourceUID) {
			kind, _, _ := expressions.Describe(q.Model)
			classic = kind == "classic_conditions"
		}
	}
	for _, frame := range condition.Frames {
		rows, err := frame.RowLen()
		if err != nil {
			return nil, err
		}
		if len(frame.Fields) == 0 {
			continue
		}
		if len(frame.Fields) != 1 || !frame.Fields[0].Type().Numeric() || rows > 1 {
			return nil, errors.New("alert condition requires one reduced numeric value per labelled frame")
		}
		v := Value{Labels: frame.Fields[0].Labels.Copy(), Missing: rows == 0}
		if rows > 0 {
			n, err := frame.Fields[0].NullableFloatAt(0)
			if err != nil {
				return nil, err
			}
			v.Missing = n == nil
			if n != nil {
				v.Value = *n
			}
		}
		if frame.Meta != nil && frame.Meta.Custom != nil {
			v.Matches, err = json.Marshal(frame.Meta.Custom)
			if err != nil {
				return nil, err
			}
			if len(v.Matches) > 1<<20 {
				return nil, errors.New("alert match diagnostics exceed 1 MiB")
			}
		}
		if classic {
			v.Captures, v.EvaluationString, err = classicCaptures(p.Condition, v.Matches)
		} else {
			v.Captures, v.EvaluationString, err = index.match(v.Labels)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		if len(out) > 1000 {
			return nil, errors.New("alert graph exceeds 1000 instances")
		}
	}
	return out, nil
}
