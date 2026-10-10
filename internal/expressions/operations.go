package expressions

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend/gtime"
)

type queryModel struct {
	Type        string `json:"type"`
	Expression  string `json:"expression"`
	Reducer     string `json:"reducer"`
	Window      string `json:"window"`
	Downsampler string `json:"downsampler"`
	Upsampler   string `json:"upsampler"`
	Invert      bool   `json:"invert"`
	Settings    *struct {
		Mode    string   `json:"mode"`
		Replace *float64 `json:"replaceWithValue"`
	} `json:"settings"`
	Conditions []conditionModel `json:"conditions"`
}

type conditionModel struct {
	UnloadEvaluator json.RawMessage `json:"unloadEvaluator"`
	Evaluator       struct {
		Type   string    `json:"type"`
		Params []float64 `json:"params"`
	} `json:"evaluator"`
	Operator struct {
		Type string `json:"type"`
	} `json:"operator"`
	Query struct {
		Params []string `json:"params"`
	} `json:"query"`
	Reducer struct {
		Type string `json:"type"`
	} `json:"reducer"`
}

var reducers = map[string]bool{"sum": true, "mean": true, "min": true, "max": true, "count": true, "last": true, "median": true}

type operation struct {
	model        queryModel
	root         *mathNode
	dependencies []string
	interval     time.Duration
}

func compile(m queryModel) (operation, error) {
	o := operation{model: m}
	switch m.Type {
	case "classic_conditions":
		return compileClassic(m)
	case "math":
		root, refs, err := parseMath(m.Expression)
		o.root = root
		o.dependencies = refs
		return o, err
	case "reduce", "resample", "threshold":
		ref := strings.TrimPrefix(m.Expression, "$")
		if strings.HasPrefix(ref, "{") && strings.HasSuffix(ref, "}") {
			ref = ref[1 : len(ref)-1]
		}
		if ref == "" || len(ref) > 100 {
			return o, errors.New("expression needs a query reference")
		}
		o.dependencies = []string{ref}
	default:
		return o, fmt.Errorf("unsupported expression operation %q", m.Type)
	}
	if m.Type == "reduce" {
		if !reducers[m.Reducer] {
			return o, fmt.Errorf("unknown expression reducer %q", m.Reducer)
		}
		if m.Settings != nil {
			switch m.Settings.Mode {
			case "": // an omitted mode is strict
			case "dropNN":
			case "replaceNN":
				if m.Settings.Replace == nil || nonNumber(m.Settings.Replace) {
					return o, errors.New("replaceNN needs a finite replaceWithValue")
				}
			default:
				return o, fmt.Errorf("unknown reduce mode %q", m.Settings.Mode)
			}
		}
	}
	if m.Type == "resample" {
		interval, err := gtime.ParseDuration(m.Window)
		if err != nil || interval <= 0 {
			return o, errors.New("resample window must be a positive duration")
		}
		o.interval = interval
		if !slices.Contains([]string{"sum", "mean", "min", "max", "last"}, m.Downsampler) {
			return o, fmt.Errorf("unknown resample downsampler %q", m.Downsampler)
		}
		if !slices.Contains([]string{"pad", "backfilling", "fillna"}, m.Upsampler) {
			return o, fmt.Errorf("unknown resample upsampler %q", m.Upsampler)
		}
	}
	if m.Type == "threshold" {
		if len(m.Conditions) != 1 {
			return o, errors.New("threshold needs one evaluator")
		}
		e := m.Conditions[0].Evaluator
		if raw := strings.TrimSpace(string(m.Conditions[0].UnloadEvaluator)); raw != "" && raw != "null" {
			return o, errors.New("stateful hysteresis thresholds are not implemented")
		}
		arity := 1
		switch e.Type {
		case "gt", "lt", "eq", "ne", "gte", "lte":
		case "within_range", "outside_range", "within_range_included", "outside_range_included":
			arity = 2
		default:
			return o, fmt.Errorf("unknown threshold evaluator %q", e.Type)
		}
		if len(e.Params) != arity {
			return o, fmt.Errorf("threshold %s needs %d parameters", e.Type, arity)
		}
		for _, n := range e.Params {
			if math.IsNaN(n) || math.IsInf(n, 0) {
				return o, errors.New("threshold parameters must be finite")
			}
		}
	}
	return o, nil
}
func reduced(points []*float64, reducer string) *float64 {
	if reducer == "count" {
		return ptr(float64(len(points)))
	}
	if reducer == "last" {
		if len(points) == 0 {
			return ptr(math.NaN())
		}
		return points[len(points)-1]
	}
	if reducer == "sum" && len(points) == 0 {
		return ptr(0)
	}
	if len(points) == 0 {
		return ptr(math.NaN())
	}
	numbers := make([]float64, 0, len(points))
	sum := 0.0
	for _, p := range points {
		if p == nil || math.IsNaN(*p) {
			return ptr(math.NaN())
		}
		sum += *p
		numbers = append(numbers, *p)
	}
	switch reducer {
	case "sum":
		return ptr(sum)
	case "mean":
		return ptr(sum / float64(len(numbers)))
	case "min":
		return ptr(slices.Min(numbers))
	case "max":
		return ptr(slices.Max(numbers))
	case "median":
		slices.Sort(numbers)
		middle := len(numbers) / 2
		if len(numbers)%2 == 1 {
			return ptr(numbers[middle])
		}
		return ptr((numbers[middle-1] + numbers[middle]) / 2)
	}
	panic("unvalidated expression reducer")
}
func (o operation) execute(vars map[string]values, from, to time.Time, b *budget) (values, error) {
	if o.model.Type == "classic_conditions" {
		return o.executeClassic(vars, b)
	}
	if o.root != nil {
		return o.root.evaluate(vars, b)
	}
	input := vars[o.dependencies[0]]
	output := values{}
	for _, v := range input {
		if err := b.step(); err != nil {
			return nil, err
		}
		switch o.model.Type {
		case "reduce":
			if v.scalar {
				return nil, errors.New("reduce requires series or number data, not a math scalar")
			}
			points := v.points
			if settings := o.model.Settings; settings != nil && settings.Mode != "" {
				points = make([]*float64, 0, len(v.points))
				for _, n := range v.points {
					if err := b.step(); err != nil {
						return nil, err
					}
					if nonNumber(n) {
						if settings.Mode == "dropNN" {
							continue
						}
						points = append(points, settings.Replace)
					} else {
						points = append(points, n)
					}
				}
			}
			if v.times == nil && len(points) == 0 {
				continue
			}
			var n *float64
			if v.times == nil {
				n = points[0]
			} else {
				n = reduced(points, o.model.Reducer)
			}
			if settings := o.model.Settings; settings != nil && nonNumber(n) {
				if settings.Mode == "replaceNN" {
					n = settings.Replace
				}
				if settings.Mode == "dropNN" {
					n = ptr(math.NaN())
				}
			}
			if err := b.allocate(1); err != nil {
				return nil, err
			}
			output = append(output, value{labels: v.labels.Copy(), points: []*float64{n}})
		case "resample":
			if v.times == nil {
				return nil, errors.New("resample requires time series input")
			}
			count := int(to.Sub(from) / o.interval)
			if count < 1 {
				return nil, errors.New("resample range is shorter than the window")
			}
			if err := b.allocate(count + 1); err != nil {
				return nil, err
			}
			res := value{labels: v.labels.Copy(), times: make([]time.Time, count+1), points: make([]*float64, count+1)}
			position := 0
			var last *float64
			for i := 0; i <= count; i++ {
				if err := b.step(); err != nil {
					return nil, err
				}
				t := from.Add(time.Duration(i) * o.interval)
				res.times[i] = t
				begin := position
				for position < len(v.times) && !v.times[position].After(t) {
					last = v.points[position]
					position++
				}
				if begin != position {
					if position-begin == 1 {
						res.points[i] = v.points[begin]
					} else {
						res.points[i] = reduced(v.points[begin:position], o.model.Downsampler)
					}
				} else {
					switch o.model.Upsampler {
					case "pad":
						res.points[i] = last
					case "backfilling":
						if position < len(v.points) {
							res.points[i] = v.points[position]
						}
					}
				}
			}
			output = append(output, res)
		case "threshold":
			mapped, err := clonePoints(v, b, func(n *float64) *float64 {
				if n != nil {
					e := o.model.Conditions[0].Evaluator
					x, a := *n, e.Params[0]
					yes := false
					switch e.Type {
					case "gt":
						yes = x > a
					case "lt":
						yes = x < a
					case "eq":
						yes = x == a
					case "ne":
						yes = x != a
					case "gte":
						yes = x >= a
					case "lte":
						yes = x <= a
					case "within_range":
						yes = x > a && x < e.Params[1]
					case "outside_range":
						yes = x < a || x > e.Params[1]
					case "within_range_included":
						yes = x >= a && x <= e.Params[1]
					case "outside_range_included":
						yes = x <= a || x >= e.Params[1]
					}
					if o.model.Invert {
						yes = !yes
					}
					n = boolean(yes)
				}
				return n
			})
			if err != nil {
				return nil, err
			}
			mapped.labels = v.labels.Copy()
			output = append(output, mapped)
		}
	}
	return output, nil
}
