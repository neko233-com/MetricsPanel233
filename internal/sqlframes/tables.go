package sqlframes

import (
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	mysql "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/grafana/grafana-plugin-sdk-go/data"
)

type frameDatabase struct {
	tables map[string]*frameTable
	names  []string
}
type provider struct{ db *frameDatabase }

func (p provider) Database(*mysql.Context, string) (mysql.Database, error) { return p.db, nil }
func (p provider) HasDatabase(*mysql.Context, string) bool                 { return true }
func (p provider) AllDatabases(*mysql.Context) []mysql.Database            { return []mysql.Database{p.db} }
func (d *frameDatabase) Name() string                                      { return "frames" }
func (d *frameDatabase) GetTableNames(*mysql.Context) ([]string, error) {
	return append([]string(nil), d.names...), nil
}
func (d *frameDatabase) GetTableInsensitive(_ *mysql.Context, name string) (mysql.Table, bool, error) {
	t, ok := d.tables[strings.ToLower(name)]
	return t, ok, nil
}

type frameTable struct {
	frame  *data.Frame
	schema mysql.Schema
}

func (t *frameTable) Name() string                 { return t.frame.RefID }
func (t *frameTable) String() string               { return t.Name() }
func (t *frameTable) Schema() mysql.Schema         { return t.schema }
func (t *frameTable) Collation() mysql.CollationID { return mysql.Collation_Unspecified }
func (t *frameTable) Partitions(*mysql.Context) (mysql.PartitionIter, error) {
	return &singlePartition{}, nil
}
func (t *frameTable) PartitionRows(*mysql.Context, mysql.Partition) (mysql.RowIter, error) {
	return &frameRows{table: t}, nil
}

type partition string

func (p partition) Key() []byte { return []byte(p) }

type singlePartition struct{ emitted bool }

func (p *singlePartition) Next(*mysql.Context) (mysql.Partition, error) {
	if p.emitted {
		return nil, io.EOF
	}
	p.emitted = true
	return partition("frame"), nil
}
func (*singlePartition) Close(*mysql.Context) error { return nil }

type frameRows struct {
	table *frameTable
	row   int
}

func (r *frameRows) Next(ctx *mysql.Context) (mysql.Row, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.row >= r.table.frame.Rows() {
		return nil, io.EOF
	}
	values := make(mysql.Row, len(r.table.frame.Fields))
	for i, field := range r.table.frame.Fields {
		if field.NilAt(r.row) {
			continue
		}
		value, ok := field.ConcreteAt(r.row)
		if !ok {
			continue
		}
		if n, ok := value.(float64); ok && (math.IsNaN(n) || math.IsInf(n, 0)) {
			continue
		}
		if n, ok := value.(float32); ok && (math.IsNaN(float64(n)) || math.IsInf(float64(n), 0)) {
			continue
		}
		// Grafana exposes native scalar/time values to the evaluator. Eager
		// Timestamp conversion would discard subsecond input before an explicit
		// DATETIME(6) cast can preserve it. JSON needs the SQL wrapper.
		if field.Type() == data.FieldTypeJSON || field.Type() == data.FieldTypeNullableJSON {
			converted, _, err := types.JSON.Convert(ctx, value)
			if err != nil {
				return nil, fmt.Errorf("SQL input %s column %s: %w", r.table.Name(), field.Name, err)
			}
			value = converted
		}
		values[i] = value
	}
	r.row++
	return values, nil
}
func (*frameRows) Close(*mysql.Context) error { return nil }

func inputType(kind data.FieldType) (mysql.Type, error) {
	switch kind {
	case data.FieldTypeInt8, data.FieldTypeNullableInt8:
		return types.Int8, nil
	case data.FieldTypeUint8, data.FieldTypeNullableUint8:
		return types.Uint8, nil
	case data.FieldTypeInt16, data.FieldTypeNullableInt16:
		return types.Int16, nil
	case data.FieldTypeUint16, data.FieldTypeNullableUint16:
		return types.Uint16, nil
	case data.FieldTypeInt32, data.FieldTypeNullableInt32:
		return types.Int32, nil
	case data.FieldTypeUint32, data.FieldTypeNullableUint32:
		return types.Uint32, nil
	case data.FieldTypeInt64, data.FieldTypeNullableInt64:
		return types.Int64, nil
	case data.FieldTypeUint64, data.FieldTypeNullableUint64:
		return types.Uint64, nil
	case data.FieldTypeFloat32, data.FieldTypeNullableFloat32:
		return types.Float32, nil
	case data.FieldTypeFloat64, data.FieldTypeNullableFloat64:
		return types.Float64, nil
	case data.FieldTypeString, data.FieldTypeNullableString:
		return types.Text, nil
	case data.FieldTypeBool, data.FieldTypeNullableBool:
		return types.Boolean, nil
	case data.FieldTypeTime, data.FieldTypeNullableTime:
		return types.Timestamp, nil
	case data.FieldTypeJSON, data.FieldTypeNullableJSON:
		return types.JSON, nil
	}
	return nil, errors.New("unsupported SQL input field type: " + kind.String())
}
