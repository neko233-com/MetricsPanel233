// Package alerttemplates implements Grafana rule template data and functions
// over the upstream Prometheus text expander, with bounded execution/output.
package alerttemplates

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
)

type Labels map[string]string

func (ls Labels) String() string {
	keys := slices.Sorted(maps.Keys(ls))
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+ls[key])
	}
	return strings.Join(parts, ", ")
}

type Capture struct {
	Labels     map[string]string
	Value      *float64
	Datasource bool
	Type       string
}

type Value struct {
	Labels Labels
	Value  float64
}

func (v Value) String() string { return strconv.FormatFloat(v.Value, 'f', -1, 64) }

type Data struct {
	Labels Labels
	Values map[string]Value
	Value  any
}

func NewData(labels map[string]string, captures map[string]Capture, evaluation string) Data {
	d := Data{Labels: Labels(maps.Clone(labels)), Values: map[string]Value{}, Value: evaluation}
	datasources, single := 0, math.NaN()
	for ref, capture := range captures {
		v := math.NaN()
		if capture.Value != nil {
			v = *capture.Value
		}
		d.Values[ref] = Value{Labels: Labels(maps.Clone(capture.Labels)), Value: v}
		if capture.Datasource {
			datasources++
			single = v
		}
	}
	if datasources == 1 {
		d.Value = single
	}
	return d
}

func Evaluation(ref string, c Capture) string {
	value := "null"
	if c.Value != nil {
		value = fmt.Sprint(*c.Value)
	}
	kind := ""
	if c.Type != "" {
		kind = "type='" + c.Type + "' "
	}
	return fmt.Sprintf("[ var='%s' labels={%s} %svalue=%s ]", ref, Labels(c.Labels), kind, value)
}
