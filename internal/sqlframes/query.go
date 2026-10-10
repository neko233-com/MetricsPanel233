package sqlframes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
	"time"

	sqle "github.com/dolthub/go-mysql-server"
	mysql "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/analyzer"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	"github.com/shopspring/decimal"
)

func Query(ctx context.Context, ref, query string, inputs map[string]data.Frames, at time.Time) (*data.Frame, error) {
	refs, err := References(ctx, query)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	db := &frameDatabase{tables: map[string]*frameTable{}, names: append([]string(nil), refs...)}
	cells := 0
	for _, name := range refs {
		frames, ok := inputs[name]
		if !ok {
			return nil, fmt.Errorf("SQL missing query reference %q", name)
		}
		frame, err := ToTable(ctx, name, frames)
		if err != nil {
			return nil, err
		}
		rows, err := frame.RowLen()
		if err != nil {
			return nil, err
		}
		if len(frame.Fields) > InputCells || rows > InputCells/max(1, len(frame.Fields)) || cells > InputCells-rows*len(frame.Fields) {
			return nil, errors.New("SQL input exceeds 100000 cells")
		}
		cells += rows * len(frame.Fields)
		key := strings.ToLower(name)
		if db.tables[key] != nil {
			return nil, fmt.Errorf("SQL query references collide ignoring case: %s", name)
		}
		table := &frameTable{frame: frame}
		seen := map[string]bool{}
		for _, field := range frame.Fields {
			if field.Name == "" || seen[strings.ToLower(field.Name)] {
				return nil, errors.New("SQL input columns need unique nonempty names")
			}
			seen[strings.ToLower(field.Name)] = true
			kind, err := inputType(field.Type())
			if err != nil {
				return nil, err
			}
			table.schema = append(table.schema, &mysql.Column{Name: field.Name, Type: kind, Nullable: field.Type().Nullable(), Source: key})
		}
		db.tables[key] = table
	}
	slices.Sort(db.names)
	session := mysql.NewBaseSession()
	mCtx := mysql.NewContext(ctx, mysql.WithSession(session), mysql.WithDisableFileReads(true), mysql.WithDisableFileWrites(true), mysql.WithTraceRedaction(true))
	mCtx.SetCurrentDatabase("frames")
	mCtx.SetQueryTime(at)
	engine := sqle.New(analyzer.NewDefault(provider{db}), &sqle.Config{IsReadOnly: true})
	defer engine.Close()
	schema, rows, _, err := engine.Query(mCtx, query)
	if err != nil {
		return nil, fmt.Errorf("SQL expression %s: %w", ref, err)
	}
	defer rows.Close(mCtx)
	frame := data.NewFrame(ref)
	frame.RefID = ref
	frame.Meta = &data.FrameMeta{Type: data.FrameTypeTable, TypeVersion: data.FrameTypeVersion{0, 1}}
	for _, column := range schema {
		kind, err := outputType(column)
		if err != nil {
			return nil, err
		}
		field := data.NewFieldFromFieldType(kind, 0)
		field.Name = column.Name
		frame.Fields = append(frame.Fields, field)
	}
	count := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row, err := rows.Next(mCtx)
		if errors.Is(err, io.EOF) {
			return frame, nil
		}
		if err != nil {
			return nil, err
		}
		if count > OutputCells-len(row) {
			frame.AppendNotices(data.Notice{Severity: data.NoticeSeverityWarning, Text: fmt.Sprintf("Query exceeded max output cells (%d). Only %d cells returned.", OutputCells, count)})
			return frame, nil
		}
		for i, value := range row {
			converted, _, err := schema[i].Type.Convert(mCtx, value)
			if err != nil {
				return nil, err
			}
			if n, ok := converted.(decimal.Decimal); ok {
				converted, _ = n.Float64()
			}
			if n, ok := converted.(float64); ok && (math.IsNaN(n) || math.IsInf(n, 0)) {
				converted = nil
			}
			if n, ok := converted.(float32); ok && (math.IsNaN(float64(n)) || math.IsInf(float64(n), 0)) {
				converted = nil
			}
			if value, ok := converted.(mysql.JSONWrapper); ok {
				var document any
				document, err = value.ToInterface(ctx)
				if err == nil {
					var raw []byte
					raw, err = json.Marshal(document)
					converted = json.RawMessage(raw)
				}
				if err != nil {
					return nil, err
				}
			}
			if err := appendValue(frame.Fields[i], converted); err != nil {
				return nil, fmt.Errorf("SQL output %s: %w", schema[i].Name, err)
			}
		}
		count += len(row)
	}
}

func outputType(column *mysql.Column) (data.FieldType, error) {
	var kind data.FieldType
	switch column.Type {
	case types.Int8:
		kind = data.FieldTypeInt8
	case types.Uint8:
		kind = data.FieldTypeUint8
	case types.Int16:
		kind = data.FieldTypeInt16
	case types.Uint16:
		kind = data.FieldTypeUint16
	case types.Int32:
		kind = data.FieldTypeInt32
	case types.Uint32:
		kind = data.FieldTypeUint32
	case types.Int64:
		kind = data.FieldTypeInt64
	case types.Uint64:
		kind = data.FieldTypeUint64
	case types.Float32:
		kind = data.FieldTypeFloat32
	case types.Float64:
		kind = data.FieldTypeFloat64
	case types.Timestamp, types.Datetime:
		kind = data.FieldTypeTime
	case types.Boolean:
		kind = data.FieldTypeBool
	case types.JSON:
		kind = data.FieldTypeJSON
	default:
		if types.IsDecimal(column.Type) {
			kind = data.FieldTypeFloat64
		} else if types.IsText(column.Type) {
			kind = data.FieldTypeString
		} else {
			return 0, fmt.Errorf("unsupported SQL output type %s", column.Type.String())
		}
	}
	if column.Nullable || kind == data.FieldTypeFloat32 || kind == data.FieldTypeFloat64 {
		kind = kind.NullableType()
	}
	return kind, nil
}
