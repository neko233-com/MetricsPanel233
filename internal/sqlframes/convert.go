package sqlframes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/data"
)

// ToTable preserves ordinary tables and expands dataplane metric frames into
// Grafana's full-long columns. The returned frame never changes an input frame.
func ToTable(ctx context.Context, ref string, frames data.Frames) (*data.Frame, error) {
	if len(frames) == 0 {
		f := data.NewFrame(ref)
		f.RefID = ref
		return f, nil
	}
	for _, f := range frames {
		if f == nil {
			return nil, errors.New("SQL input contains a nil frame")
		}
		if _, err := f.RowLen(); err != nil {
			return nil, err
		}
	}
	var kind data.FrameType
	if frames[0].Meta != nil {
		kind = frames[0].Meta.Type
	}
	numeric := kind == data.FrameTypeNumericMulti || kind == data.FrameTypeNumericWide
	series := kind == data.FrameTypeTimeSeriesMulti || kind == data.FrameTypeTimeSeriesWide
	if !numeric && !series {
		if len(frames) != 1 {
			return nil, fmt.Errorf("SQL input %s needs one table or a supported wide/multi frame type", ref)
		}
		for _, field := range frames[0].Fields {
			if len(field.Labels) > 0 {
				return nil, fmt.Errorf("SQL input %s has labels without a supported frame type", ref)
			}
		}
		f := *frames[0]
		f.RefID = ref
		return &f, nil
	}
	if (kind == data.FrameTypeNumericWide || kind == data.FrameTypeTimeSeriesWide) && len(frames) != 1 {
		return nil, errors.New("SQL wide input requires exactly one frame")
	}
	keys := map[string]bool{}
	hasDisplay, rowCount := false, 0
	for _, frame := range frames {
		if numeric && frame.Rows() > 1 {
			return nil, errors.New("SQL numeric input requires at most one row per frame")
		}
		for _, field := range frame.Fields {
			if !field.Type().Numeric() {
				continue
			}
			if rowCount > InputCells-field.Len() {
				return nil, errors.New("SQL input exceeds 100000 cells")
			}
			rowCount += field.Len()
			for key := range field.Labels {
				keys[key] = true
			}
			hasDisplay = hasDisplay || field.Config != nil && field.Config.DisplayNameFromDS != ""
		}
	}
	columns := 2 + len(keys)
	if series {
		columns++
	}
	if hasDisplay {
		columns++
	}
	if columns > InputCells || rowCount > InputCells/columns {
		return nil, errors.New("SQL input exceeds 100000 cells")
	}
	labelKeys := make([]string, 0, len(keys))
	for key := range keys {
		labelKeys = append(labelKeys, key)
	}
	slices.Sort(labelKeys)
	type metricRow struct {
		at    time.Time
		field *data.Field
		index int
	}
	rows := make([]metricRow, 0, rowCount)
	for _, frame := range frames {
		var clock *data.Field
		if series {
			for _, field := range frame.Fields {
				if field.Type() == data.FieldTypeTime {
					clock = field
					break
				}
			}
			if clock == nil {
				return nil, errors.New("SQL series input needs a nonnullable time field")
			}
		}
		for _, field := range frame.Fields {
			if !field.Type().Numeric() {
				continue
			}
			for i := 0; i < field.Len(); i++ {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				r := metricRow{field: field, index: i}
				if clock != nil {
					r.at = clock.At(i).(time.Time)
				}
				rows = append(rows, r)
			}
		}
	}
	if series {
		slices.SortStableFunc(rows, func(a, b metricRow) int {
			if result := a.at.Compare(b.at); result != 0 {
				return result
			}
			if a.field.Name < b.field.Name {
				return -1
			}
			if a.field.Name > b.field.Name {
				return 1
			}
			return 0
		})
	}
	f := data.NewFrame(ref)
	f.RefID = ref
	f.Meta = &data.FrameMeta{Type: "numeric-full-long"}
	if series {
		f.Meta.Type = "timeseries-full-long"
		f.Fields = append(f.Fields, data.NewField("time", nil, []time.Time{}))
	}
	if series {
		f.Fields = append(f.Fields, data.NewField("__value__", nil, []*float64{}), data.NewField("__metric_name__", nil, []string{}))
	} else {
		f.Fields = append(f.Fields, data.NewField("__metric_name__", nil, []string{}), data.NewField("__value__", nil, []*float64{}))
	}
	if hasDisplay {
		f.Fields = append(f.Fields, data.NewField("__display_name__", nil, []*string{}))
	}
	for _, key := range labelKeys {
		f.Fields = append(f.Fields, data.NewField(key, nil, []*string{}))
	}
	for _, row := range rows {
		value, err := row.field.NullableFloatAt(row.index)
		if err != nil {
			return nil, err
		}
		i := 0
		if series {
			f.Fields[i].Append(row.at)
			i++
			f.Fields[i].Append(value)
			i++
			f.Fields[i].Append(row.field.Name)
			i++
		} else {
			f.Fields[i].Append(row.field.Name)
			i++
			f.Fields[i].Append(value)
			i++
		}
		if hasDisplay {
			var display *string
			if row.field.Config != nil && row.field.Config.DisplayNameFromDS != "" {
				name := row.field.Config.DisplayNameFromDS
				display = &name
			}
			f.Fields[i].Append(display)
			i++
		}
		for _, key := range labelKeys {
			var label *string
			if value, ok := row.field.Labels[key]; ok {
				label = &value
			}
			f.Fields[i].Append(label)
			i++
		}
	}
	return f, nil
}

func typedValue[T any](value any, nullable bool) (any, error) {
	n, ok := value.(T)
	if !ok {
		return nil, fmt.Errorf("unexpected SQL value type %T", value)
	}
	if nullable {
		return &n, nil
	}
	return n, nil
}

func appendValue(field *data.Field, value any) error {
	if value == nil {
		if !field.Type().Nullable() {
			// Aggregate analyzers can declare MAX/MIN nonnullable even when
			// every input is NULL. Preserve the actual SQL result in that case.
			promoted := data.NewFieldFromFieldType(field.Type().NullableType(), 0)
			promoted.Name, promoted.Labels, promoted.Config = field.Name, field.Labels, field.Config
			for i := 0; i < field.Len(); i++ {
				previous, _ := field.ConcreteAt(i)
				if err := appendValue(promoted, previous); err != nil {
					return err
				}
			}
			*field = *promoted
		}
		field.Append(nil)
		return nil
	}
	var converted any
	var err error
	nullable := field.Type().Nullable()
	switch field.Type() {
	case data.FieldTypeInt8, data.FieldTypeNullableInt8:
		converted, err = typedValue[int8](value, nullable)
	case data.FieldTypeUint8, data.FieldTypeNullableUint8:
		converted, err = typedValue[uint8](value, nullable)
	case data.FieldTypeInt16, data.FieldTypeNullableInt16:
		converted, err = typedValue[int16](value, nullable)
	case data.FieldTypeUint16, data.FieldTypeNullableUint16:
		converted, err = typedValue[uint16](value, nullable)
	case data.FieldTypeInt32, data.FieldTypeNullableInt32:
		converted, err = typedValue[int32](value, nullable)
	case data.FieldTypeUint32, data.FieldTypeNullableUint32:
		converted, err = typedValue[uint32](value, nullable)
	case data.FieldTypeInt64, data.FieldTypeNullableInt64:
		converted, err = typedValue[int64](value, nullable)
	case data.FieldTypeUint64, data.FieldTypeNullableUint64:
		converted, err = typedValue[uint64](value, nullable)
	case data.FieldTypeFloat32, data.FieldTypeNullableFloat32:
		converted, err = typedValue[float32](value, nullable)
	case data.FieldTypeFloat64, data.FieldTypeNullableFloat64:
		converted, err = typedValue[float64](value, nullable)
	case data.FieldTypeTime, data.FieldTypeNullableTime:
		converted, err = typedValue[time.Time](value, nullable)
	case data.FieldTypeString, data.FieldTypeNullableString:
		converted, err = typedValue[string](value, nullable)
	case data.FieldTypeBool, data.FieldTypeNullableBool:
		if v, ok := value.(int8); ok {
			value = v != 0
		}
		converted, err = typedValue[bool](value, nullable)
	case data.FieldTypeJSON, data.FieldTypeNullableJSON:
		converted, err = typedValue[json.RawMessage](value, nullable)
	default:
		err = fmt.Errorf("unsupported SQL field type %s", field.Type())
	}
	if err != nil {
		return err
	}
	field.Append(converted)
	return nil
}
