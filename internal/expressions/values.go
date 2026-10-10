// Package expressions evaluates Grafana server expression graphs over SDK frames.
// The implementation follows the public 13.2.3 contracts without rewriting math
// into PromQL, so backend plugins and local metrics have the same semantics.
package expressions

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/data"
)

const MaxPoints = 1000000
const MaxItems = 10000

type value struct {
	name   string // original numeric field name, used in classic match diagnostics
	meta   any
	labels data.Labels
	times  []time.Time // nil identifies a number; non-nil identifies a series
	points []*float64
	scalar bool
}
type values []value
type budget struct {
	ctx          context.Context
	points, work int
}

func (b *budget) allocate(n int) error {
	if n < 0 || b.points > MaxPoints-n {
		return errors.New("expression graph exceeds 1000000 working points")
	}
	b.points += n
	return b.ctx.Err()
}
func (b *budget) step() error {
	b.work++
	if b.work > 2000000 {
		return errors.New("expression graph exceeds computation limit")
	}
	if b.work%256 == 0 {
		return b.ctx.Err()
	}
	return nil
}
func ptr(n float64) *float64 { return &n }
func fromFrames(frames data.Frames, sourceType string, b *budget) (values, error) {
	var result values
	for _, frame := range frames {
		if frame == nil {
			return nil, errors.New("nil datasource frame")
		}
		if _, err := frame.RowLen(); err != nil {
			return nil, err
		}
		if len(frame.Fields) == 0 {
			continue
		}
		timeIndex := -1
		numbers, strings := []*data.Field{}, []*data.Field{}
		for i, field := range frame.Fields {
			switch {
			case field.Type().Time():
				if timeIndex >= 0 {
					return nil, errors.New("expression input has multiple time fields")
				}
				timeIndex = i
			case field.Type().Numeric():
				numbers = append(numbers, field)
			case field.Type() == data.FieldTypeString || field.Type() == data.FieldTypeNullableString:
				strings = append(strings, field)
			default:
				return nil, fmt.Errorf("expression input field %q has unsupported type %s", field.Name, field.Type())
			}
		}
		// Grafana represents Prometheus instant vectors as numbers, even when their
		// transport frame has a one-row time field.
		vector := false
		if sourceType == "prometheus" && frame.Meta != nil {
			switch custom := frame.Meta.Custom.(type) {
			case map[string]string:
				vector = custom["resultType"] == "vector"
			case map[string]any:
				vector = custom["resultType"] == "vector"
			}
		}
		if timeIndex < 0 || vector {
			if len(numbers) != 1 {
				return nil, errors.New("expression numeric table needs exactly one numeric field")
			}
			for row := 0; row < frame.Rows(); row++ {
				if err := b.allocate(1); err != nil {
					return nil, err
				}
				labels := numbers[0].Labels.Copy()
				if sourceType == "prometheus" {
					delete(labels, "__name__")
				}
				for _, field := range strings {
					label, ok := field.ConcreteAt(row)
					if !ok {
						return nil, errors.New("expression numeric table has a null label")
					}
					if labels == nil {
						labels = data.Labels{}
					}
					if _, duplicate := labels[field.Name]; duplicate {
						return nil, errors.New("expression numeric table has duplicate labels")
					}
					labels[field.Name] = label.(string)
				}
				n, err := numbers[0].NullableFloatAt(row)
				if err != nil {
					return nil, err
				}
				name := numbers[0].Name
				if vector {
					name = frame.Name
				}
				result = append(result, value{name: name, labels: labels, points: []*float64{n}})
			}
		} else {
			if len(numbers) == 0 || len(strings) > 0 {
				return nil, errors.New("expression time series must be wide with numeric value fields")
			}
			times := make([]time.Time, frame.Rows())
			for row := range times {
				t, ok := frame.Fields[timeIndex].ConcreteAt(row)
				if !ok {
					return nil, errors.New("expression series has a null timestamp")
				}
				times[row] = t.(time.Time)
				if row > 0 && !times[row].After(times[row-1]) {
					return nil, errors.New("expression timestamps must be strictly increasing")
				}
			}
			for _, field := range numbers {
				if err := b.allocate(frame.Rows()); err != nil {
					return nil, err
				}
				points := make([]*float64, frame.Rows())
				for row := range points {
					if err := b.step(); err != nil {
						return nil, err
					}
					n, err := field.NullableFloatAt(row)
					if err != nil {
						return nil, err
					}
					points[row] = n
				}
				labels := field.Labels.Copy()
				if sourceType == "prometheus" {
					delete(labels, "__name__")
				}
				result = append(result, value{name: field.Name, labels: labels, times: times, points: points})
			}
		}
		if len(result) > MaxItems {
			return nil, errors.New("expression input exceeds 10000 items")
		}
	}
	return result, nil
}
func toFrames(ref string, input values) data.Frames {
	if len(input) == 0 {
		f := data.NewFrame("")
		f.RefID = ref
		return data.Frames{f}
	}
	result := make(data.Frames, 0, len(input))
	for _, v := range input {
		var f *data.Frame
		if v.times == nil {
			f = data.NewFrame("", data.NewField(ref, v.labels, v.points))
			f.Meta = &data.FrameMeta{Type: data.FrameTypeNumericMulti, TypeVersion: data.FrameTypeVersion{0, 1}}
		} else {
			f = data.NewFrame("", data.NewField("Time", nil, v.times), data.NewField(ref, v.labels, v.points))
			f.Meta = &data.FrameMeta{Type: data.FrameTypeTimeSeriesMulti, TypeVersion: data.FrameTypeVersion{0, 1}}
		}
		f.RefID = ref
		f.Meta.Custom = v.meta
		result = append(result, f)
	}
	return result
}
func nonNumber(n *float64) bool { return n == nil || math.IsNaN(*n) || math.IsInf(*n, 0) }
func clonePoints(v value, b *budget, fn func(*float64) *float64) (value, error) {
	if err := b.allocate(len(v.points)); err != nil {
		return value{}, err
	}
	output := v
	output.points = make([]*float64, len(v.points))
	for i, n := range v.points {
		if err := b.step(); err != nil {
			return value{}, err
		}
		output.points[i] = fn(n)
	}
	return output, nil
}
func joinedLabels(a, b data.Labels) (data.Labels, bool) {
	if a.Equals(b) || len(a) == 0 || len(b) == 0 {
		l := a
		if len(b) > len(a) {
			l = b
		}
		return l.Copy(), true
	}
	if a.Contains(b) {
		return a.Copy(), true
	}
	if b.Contains(a) {
		return b.Copy(), true
	}
	return nil, false
}
func binaryValues(a, bb values, op string, b *budget) (values, error) {
	result := values{}
	// Equal non-empty dimension keys permit an indexed exact-label join.
	// Subset dimensions and unlabelled broadcasts retain Grafana's union rules.
	keySet := func(items values) string {
		signature := ""
		for _, v := range items {
			if len(v.labels) == 0 {
				return ""
			}
			keys := make([]string, 0, len(v.labels))
			for key := range v.labels {
				keys = append(keys, key)
			}
			slices.Sort(keys)
			current := strings.Join(keys, "\x00")
			if signature != "" && signature != current {
				return ""
			}
			signature = current
		}
		return signature
	}
	var index map[string]values
	if keys := keySet(a); keys != "" && keys == keySet(bb) {
		index = map[string]values{}
		for _, v := range bb {
			key := v.labels.String()
			index[key] = append(index[key], v)
		}
	}
	for _, left := range a {
		candidates := bb
		if index != nil {
			candidates = index[left.labels.String()]
		}
		if len(a) == 1 && len(bb) == 1 && len(candidates) == 0 {
			candidates = bb
		}
		for _, right := range candidates {
			if err := b.step(); err != nil {
				return nil, err
			}
			labels, match := joinedLabels(left.labels, right.labels)
			if !match && len(a) == 1 && len(bb) == 1 {
				labels = nil
				match = true
			}
			if !match {
				continue
			}
			if len(result) >= MaxItems {
				return nil, errors.New("expression join exceeds 10000 items")
			}
			v := value{labels: labels, scalar: left.scalar && right.scalar}
			switch {
			case left.times == nil && right.times == nil:
				if err := b.allocate(1); err != nil {
					return nil, err
				}
				v.points = []*float64{binaryNumber(left.points[0], right.points[0], op)}
			case left.times != nil && right.times != nil:
				i, j := 0, 0
				v.times = []time.Time{}
				v.points = []*float64{}
				for i < len(left.times) && j < len(right.times) {
					if err := b.step(); err != nil {
						return nil, err
					}
					if left.times[i].Equal(right.times[j]) {
						if err := b.allocate(1); err != nil {
							return nil, err
						}
						v.times = append(v.times, left.times[i])
						v.points = append(v.points, binaryNumber(left.points[i], right.points[j], op))
						i++
						j++
					} else if left.times[i].Before(right.times[j]) {
						i++
					} else {
						j++
					}
				}
			default:
				series, number, first := left, right, true
				if left.times == nil {
					series, number, first = right, left, false
				}
				var err error
				v, err = clonePoints(series, b, func(n *float64) *float64 {
					if first {
						return binaryNumber(n, number.points[0], op)
					}
					return binaryNumber(number.points[0], n, op)
				})
				if err != nil {
					return nil, err
				}
				v.labels = labels
				v.scalar = false
			}
			result = append(result, v)
		}
	}
	return result, nil
}
func binaryNumber(a, b *float64, op string) *float64 {
	if a == nil || b == nil {
		return nil
	}
	if math.IsNaN(*a) || math.IsNaN(*b) {
		return ptr(math.NaN())
	}
	x, y := *a, *b
	switch op {
	case "+":
		return ptr(x + y)
	case "-":
		return ptr(x - y)
	case "*":
		return ptr(x * y)
	case "/":
		return ptr(x / y)
	case "%":
		return ptr(math.Mod(x, y))
	case "**":
		return ptr(math.Pow(x, y))
	case "==":
		return boolean(x == y)
	case "!=":
		return boolean(x != y)
	case ">":
		return boolean(x > y)
	case "<":
		return boolean(x < y)
	case ">=":
		return boolean(x >= y)
	case "<=":
		return boolean(x <= y)
	case "&&":
		return boolean(x != 0 && y != 0)
	case "||":
		return boolean(x != 0 || y != 0)
	}
	panic("unvalidated expression operator")
}
func boolean(v bool) *float64 {
	if v {
		return ptr(1)
	}
	return ptr(0)
}
func sortedRefs(m map[string]bool) []string {
	refs := make([]string, 0, len(m))
	for ref := range m {
		refs = append(refs, ref)
	}
	slices.Sort(refs)
	return refs
}
