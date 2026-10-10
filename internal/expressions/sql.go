package expressions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	"github.com/neko233-com/MetricsPanel233/internal/sqlframes"
)

// ValidateSQLGraph applies the SQL terminal-node contract to saved alert graphs
// and interactive queries alike.
func ValidateSQLGraph(groups map[string][]backend.DataQuery) error {
	ops := map[string]operation{}
	for uid, queries := range groups {
		if !IsSource(uid) {
			continue
		}
		for _, q := range queries {
			var m queryModel
			if err := json.Unmarshal(q.JSON, &m); err != nil {
				return err
			}
			op, err := compile(m)
			if err != nil {
				return err
			}
			ops[q.RefID] = op
		}
	}
	return validateSQLOps(ops)
}

func validateSQLOps(ops map[string]operation) error {
	count := 0
	for _, op := range ops {
		if op.model.Type == "sql" {
			count++
		}
	}
	if count > 1 {
		return errors.New("only one SQL expression is allowed per query graph")
	}
	refs := make([]string, 0, len(ops))
	for ref := range ops {
		refs = append(refs, ref)
	}
	slices.Sort(refs)
	for _, ref := range refs {
		op := ops[ref]
		for _, dep := range op.dependencies {
			dependency, expression := ops[dep]
			if op.model.Type == "sql" && expression {
				return fmt.Errorf("SQL expression %s can only reference backend datasource queries: %s", ref, dep)
			}
			if expression && dependency.model.Type == "sql" {
				return fmt.Errorf("expression %s cannot consume SQL result %s", ref, dep)
			}
		}
	}
	return nil
}

func sqlResult(ctx context.Context, ref string, op operation, query backend.DataQuery, inputs map[string]data.Frames) (data.Frames, error) {
	if query.TimeRange.From.IsZero() || query.TimeRange.To.IsZero() {
		return nil, errors.New("SQL expression requires a time range")
	}
	frame, err := sqlframes.Query(ctx, ref, op.model.Expression, inputs, query.TimeRange.To)
	if err != nil {
		return nil, err
	}
	if op.model.Format != "alerting" || frame.Rows() == 0 {
		return data.Frames{frame}, nil
	}
	return SQLAlertFrames(ref, frame)
}

// SQLAlertFrames gives each SQL row its own number frame. Only string columns
// become labels, and duplicate label combinations invalidate the whole result.
func SQLAlertFrames(ref string, frame *data.Frame) (data.Frames, error) {
	if _, err := frame.RowLen(); err != nil {
		return nil, err
	}
	var number *data.Field
	labels := []*data.Field{}
	for _, field := range frame.Fields {
		if field.Type().Numeric() {
			if number != nil {
				return nil, errors.New("SQL alert result requires exactly one numeric column")
			}
			number = field
		} else if field.Type() == data.FieldTypeString || field.Type() == data.FieldTypeNullableString {
			labels = append(labels, field)
		}
	}
	if number == nil {
		return nil, errors.New("SQL alert result requires exactly one numeric column")
	}
	out := make(data.Frames, 0, frame.Rows())
	seen := map[string]bool{}
	for row := 0; row < frame.Rows(); row++ {
		set := data.Labels{}
		for _, field := range labels {
			if value, ok := field.ConcreteAt(row); ok {
				set[field.Name] = value.(string)
			}
		}
		key := set.String()
		if seen[key] {
			return nil, fmt.Errorf("SQL alert result has duplicate string labels %s", key)
		}
		seen[key] = true
		// Grafana's SQL alert adapter uses FloatAt: numeric NULL becomes NaN.
		// The alert engine then applies the same nonzero/nonfinite semantics
		// and durable value_text representation as other graph expressions.
		value, err := number.FloatAt(row)
		if err != nil {
			return nil, err
		}
		field := data.NewField(number.Name, set, []*float64{&value})
		field.Config = number.Config
		f := data.NewFrame(ref, field)
		f.RefID = ref
		f.Meta = &data.FrameMeta{Type: data.FrameTypeNumericMulti, TypeVersion: data.FrameTypeVersion{0, 1}}
		out = append(out, f)
	}
	return out, nil
}
