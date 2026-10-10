package alerting

import (
	"context"
	"math"
	"strconv"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/neko233-com/MetricsPanel233/internal/alertgraph"
	"github.com/neko233-com/MetricsPanel233/internal/querycontext"
)

type PreviewValue struct {
	Labels    map[string]string `json:"labels"`
	Value     *float64          `json:"value"`
	ValueText string            `json:"value_text,omitempty"`
	Missing   bool              `json:"missing"`
	Satisfied *bool             `json:"satisfied"`
}

type GraphPreview struct {
	At        int64             `json:"at"`
	Condition string            `json:"condition"`
	Results   backend.Responses `json:"results"`
	Values    []PreviewValue    `json:"condition_values"`
	Error     string            `json:"condition_error,omitempty"`
}

// PreviewGraph shares evaluation capacity and datasource semantics with the scheduler,
// but has no rule identity, timers, state transitions, notification or recording writes.
func (e *Engine) PreviewGraph(ctx context.Context, plan *alertgraph.Plan, at time.Time) (GraphPreview, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	select {
	case e.slots <- struct{}{}:
		defer func() { <-e.slots }()
	case <-ctx.Done():
		return GraphPreview{}, ctx.Err()
	}
	graphCtx := querycontext.WithHeaders(ctx, map[string]string{"FromAlert": "true", "X-Cache-Skip": "true", "X-Grafana-Org-Id": "1"})
	response, queryErr := alertgraph.QueryFrames(graphCtx, plan, at, e.GraphSource, nil)
	if err := ctx.Err(); err != nil {
		return GraphPreview{}, err
	}
	if queryErr != nil {
		return GraphPreview{}, queryErr
	}
	preview := GraphPreview{At: at.UnixMilli(), Condition: plan.Condition, Results: response.Responses, Values: []PreviewValue{}}
	values, err := alertgraph.EvaluateResponse(graphCtx, plan, response)
	if ctx.Err() != nil {
		return GraphPreview{}, ctx.Err()
	}
	if err != nil {
		preview.Error = err.Error()
		return preview, nil
	}
	for _, item := range values {
		value := PreviewValue{Labels: item.Labels, Missing: item.Missing}
		if !item.Missing {
			truth := item.Value != 0
			value.Satisfied = &truth
			if math.IsNaN(item.Value) || math.IsInf(item.Value, 0) {
				value.ValueText = strconv.FormatFloat(item.Value, 'g', -1, 64)
			} else {
				number := item.Value
				value.Value = &number
			}
		}
		preview.Values = append(preview.Values, value)
	}
	return preview, nil
}
