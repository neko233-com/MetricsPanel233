package expressions

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"

	"github.com/grafana/grafana-plugin-sdk-go/data"
)

// Classic conditions have their own null handling and ordered boolean fold.
// Their public 13.2.3 result is one unlabelled number with match diagnostics.
var classicReducers = []string{"avg", "sum", "min", "max", "count", "last", "median", "diff", "diff_abs", "percent_diff", "percent_diff_abs", "count_non_null"}

func compileClassic(m queryModel) (operation, error) {
	o := operation{model: m}
	if len(m.Conditions) > MaxItems {
		return o, errors.New("classic conditions exceed 10000 conditions")
	}
	seen := make(map[string]bool)
	for i, c := range m.Conditions {
		if i > 0 && !slices.Contains([]string{"and", "or", "logic-or"}, c.Operator.Type) {
			return o, fmt.Errorf("condition %d operator must be and, or or logic-or", i+1)
		}
		if len(c.Query.Params) == 0 || c.Query.Params[0] == "" || len(c.Query.Params[0]) > 100 {
			return o, fmt.Errorf("condition %d needs a query RefID", i+1)
		}
		if !slices.Contains(classicReducers, c.Reducer.Type) {
			return o, fmt.Errorf("condition %d has unknown classic reducer %q", i+1, c.Reducer.Type)
		}
		e := c.Evaluator
		switch e.Type {
		case "gt", "lt", "eq", "ne", "gte", "lte":
			if len(e.Params) < 1 {
				return o, fmt.Errorf("condition %d needs a threshold parameter", i+1)
			}
		case "within_range", "outside_range", "within_range_included", "outside_range_included":
			if len(e.Params) != 2 {
				return o, fmt.Errorf("condition %d needs two range parameters", i+1)
			}
		case "no_value":
		default:
			return o, fmt.Errorf("condition %d has unknown classic evaluator %q", i+1, e.Type)
		}
		if !seen[c.Query.Params[0]] {
			o.dependencies = append(o.dependencies, c.Query.Params[0])
			seen[c.Query.Params[0]] = true
		}
	}
	return o, nil
}

type classicMatch struct {
	Value  string      `json:"value"`
	Metric string      `json:"metric"`
	Labels data.Labels `json:"labels"`
}

func newClassicMatch(n *float64, name string, labels data.Labels) classicMatch {
	text := ""
	if n != nil {
		text = strconv.FormatFloat(*n, 'f', -1, 64)
	}
	return classicMatch{Value: text, Metric: name, Labels: labels.Copy()}
}

func classicCompare(c conditionModel, n *float64) bool {
	if c.Evaluator.Type == "no_value" {
		return n == nil
	}
	if n == nil {
		return false
	}
	x, a := *n, c.Evaluator.Params[0]
	switch c.Evaluator.Type {
	case "gt":
		return x > a
	case "lt":
		return x < a
	case "eq":
		return x == a
	case "ne":
		return x != a
	case "gte":
		return x >= a
	case "lte":
		return x <= a
	}
	z := c.Evaluator.Params[1]
	switch c.Evaluator.Type {
	case "within_range":
		return (x > a && x < z) || (x > z && x < a)
	case "outside_range":
		return (x < a && x < z) || (x > a && x > z)
	case "within_range_included":
		return (x >= a && x <= z) || (x >= z && x <= a)
	case "outside_range_included":
		return (x <= a && x <= z) || (x >= a && x >= z)
	}
	return false
}

func classicReduce(points []*float64, reducer string, b *budget) (*float64, error) {
	if len(points) == 0 {
		return nil, nil
	}
	if reducer == "count" {
		return ptr(float64(len(points))), nil
	}
	count, lastIndex := 0, -1
	first, last, sum := 0.0, 0.0, 0.0
	min, max := math.MaxFloat64, -math.MaxFloat64
	var ordered []float64
	if reducer == "median" {
		if err := b.allocate(len(points)); err != nil {
			return nil, err
		}
		ordered = make([]float64, 0, len(points))
	}
	for i, n := range points {
		if err := b.step(); err != nil {
			return nil, err
		}
		if n == nil || math.IsNaN(*n) {
			continue
		}
		if count == 0 {
			first = *n
		}
		count++
		last, lastIndex = *n, i
		sum += *n
		if *n < min {
			min = *n
		}
		if *n > max {
			max = *n
		}
		if ordered != nil {
			ordered = append(ordered, *n)
		}
	}
	if count == 0 {
		return nil, nil
	}
	n := 0.0
	switch reducer {
	case "avg":
		n = sum / float64(count)
	case "sum":
		n = sum
	case "min":
		n = min
	case "max":
		n = max
	case "last":
		n = last
	case "count_non_null":
		n = float64(count)
	case "median":
		slices.Sort(ordered)
		middle := len(ordered) / 2
		n = ordered[middle]
		if len(ordered)%2 == 0 {
			n = (ordered[middle-1] + n) / 2
		}
	default:
		if lastIndex > 0 {
			n = last - first
			if reducer == "percent_diff" || reducer == "percent_diff_abs" {
				n = n / math.Abs(first) * 100
			}
			if reducer == "diff_abs" || reducer == "percent_diff_abs" {
				n = math.Abs(n)
			}
		}
	}
	return ptr(n), nil
}

func (o operation) executeClassic(vars map[string]values, b *budget) (values, error) {
	firing, noData := false, false
	matches := make([]classicMatch, 0)
	for i, c := range o.model.Conditions {
		if firing && c.Operator.Type == "logic-or" {
			break
		}
		if err := b.step(); err != nil {
			return nil, err
		}
		input := vars[c.Query.Params[0]]
		hit, missing := false, 0
		if len(input) == 0 && c.Evaluator.Type == "no_value" {
			hit = true
			matches = append(matches, newClassicMatch(nil, "", nil))
		}
		for _, v := range input {
			if err := b.step(); err != nil {
				return nil, err
			}
			if v.scalar {
				return nil, errors.New("classic conditions require series or number data, not a math scalar")
			}
			var n *float64
			if v.times != nil {
				var err error
				n, err = classicReduce(v.points, c.Reducer.Type, b)
				if err != nil {
					return nil, err
				}
			} else if len(v.points) > 0 {
				n = v.points[0]
			}
			if classicCompare(c, n) {
				hit = true
				matches = append(matches, newClassicMatch(n, v.name, v.labels))
			} else if n == nil {
				missing++
			}
		}
		empty := missing == len(input) && !(len(input) == 0 && hit)
		if empty {
			matches = append(matches, newClassicMatch(nil, "NoData", nil))
		}
		if i == 0 {
			firing, noData = hit, empty
		} else if c.Operator.Type == "and" {
			firing, noData = firing && hit, noData && empty
		} else {
			firing, noData = firing || hit, noData || empty
		}
		if len(matches) > MaxItems {
			return nil, errors.New("classic condition matches exceed 10000 items")
		}
	}
	var n *float64
	if !noData {
		n = boolean(firing)
	}
	if err := b.allocate(1); err != nil {
		return nil, err
	}
	return values{{points: []*float64{n}, meta: matches}}, nil
}
