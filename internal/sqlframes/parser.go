// Package sqlframes runs Grafana SQL expressions over isolated SDK data frames.
package sqlframes

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/dolthub/vitess/go/vt/sqlparser"
)

const QueryBytes = 10000
const InputCells = 100000
const OutputCells = 100000

var allowedFunctions = map[string]bool{}
var allowedNodes = map[string]bool{}

func init() {
	// Public Grafana 13.2.3 SQL function and syntax contracts.
	for _, name := range strings.Fields(`if coalesce ifnull nullif least sum avg count min max stddev std stddev_pop stddev_sample variance var_pop var_samp group_concat row_number rank dense_rank percent_rank first_value last_value ntile lead lag abs round floor ceiling ceil sqrt pow power mod log log2 log10 exp sign ln truncate sin cos tan cot asin acos atan atan2 conv degrees radians rand pi concat length char_length lower upper substring substring_index left right ltrim rtrim replace reverse lcase ucase mid repeat position instr locate ascii ord char elt quote from_base64 format regexp_substr regexp_replace regexp_instr regexp_like str_to_date date_format get_format date_add adddate date_sub subdate year month day weekday last_day yearweek weekofyear datediff unix_timestamp from_unixtime extract hour minute second microsecond dayname monthname dayofweek dayofmonth dayofyear week quarter time_to_sec sec_to_time timestampdiff timestampadd from_days to_days time_format time timediff cast convert json_extract json_object json_array json_valid json_merge json_merge_patch json_merge_preserve json_contains json_length json_type json_keys json_contains_path json_depth json_search json_quote json_unquote json_set json_insert json_replace json_remove json_array_append json_array_insert json_objectagg json_arrayagg json_overlaps json_pretty json_value`) {
		allowedFunctions[name] = true
	}
	for _, name := range strings.Fields(`FuncExpr AsOf AliasedExpr AliasedTableExpr AndExpr OrExpr NotExpr BinaryExpr UnaryExpr BoolVal NullVal CaseExpr When CharExpr ColName ColIdent Columns ColumnType Comments CommonTableExpr ComparisonExpr ConvertExpr ConvertType CollateExpr Exprs ExtractFuncExpr GroupConcatExpr GroupBy Frame FrameExtent FrameBound IndexHints IntervalExpr Into IsExpr JoinTableExpr JoinCondition JSONTableExpr JSONTableSpec JSONTableColDef Select SelectExprs ParenSelect SetOp StarExpr SQLVal Limit Order OrderBy Over Window WindowDef ParenExpr RangeCond Subquery TableName TableExprs TableIdent TimestampFuncExpr TrimExpr ValTuple With Where`) {
		allowedNodes[name] = true
	}
}

func References(ctx context.Context, query string) ([]string, error) {
	if query == "" || len(query) > QueryBytes {
		return nil, errors.New("SQL expression must be 1–10000 bytes")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	statement, err := sqlparser.Parse(query)
	if err != nil {
		return nil, fmt.Errorf("SQL expression: %w", err)
	}
	tables, ctes := map[string]bool{}, map[string]bool{}
	var visit func(...sqlparser.SQLNode) error
	visit = func(nodes ...sqlparser.SQLNode) error {
		return sqlparser.Walk(func(node sqlparser.SQLNode) (bool, error) {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			typeOf := reflect.TypeOf(node)
			if typeOf == nil {
				return false, nil
			}
			if typeOf.Kind() == reflect.Pointer {
				typeOf = typeOf.Elem()
			}
			if !allowedNodes[typeOf.Name()] {
				return false, fmt.Errorf("SQL syntax %s is not allowed", typeOf.Name())
			}
			switch item := node.(type) {
			case *sqlparser.FuncExpr:
				if !allowedFunctions[strings.ToLower(item.Name.String())] {
					return false, fmt.Errorf("SQL function %s is not allowed", item.Name.String())
				}
			case *sqlparser.ColName:
				if strings.HasPrefix(item.Name.String(), "@") || strings.HasPrefix(item.Qualifier.Name.String(), "@@") {
					return false, errors.New("SQL session and system variables are not allowed")
				}
			case *sqlparser.Into:
				if item != nil {
					return false, errors.New("SQL INTO is not allowed")
				}
			case *sqlparser.Select:
				if item.Lock != nil && item.Lock.Type != "" {
					return false, errors.New("SQL locking is not allowed")
				}
				if err := visit(item.Window); err != nil {
					return false, err
				}
			case *sqlparser.SetOp:
				if item.GetInto() != nil || item.Lock != nil && item.Lock.Type != "" {
					return false, errors.New("SQL INTO and locking are not allowed")
				}
				if err := visit(item.With, item.OrderBy, item.Limit); err != nil {
					return false, err
				}
			case *sqlparser.CommonTableExpr:
				ctes[strings.ToLower(item.As.String())] = true
			case *sqlparser.AliasedTableExpr:
				if table, ok := item.Expr.(sqlparser.TableName); ok {
					tables[table.Name.String()] = true
				}
			}
			return true, nil
		}, nodes...)
	}
	if err := visit(statement); err != nil {
		return nil, err
	}
	refs := []string{}
	for table := range tables {
		if !ctes[strings.ToLower(table)] && table != "dual" {
			refs = append(refs, table)
		}
	}
	slices.Sort(refs)
	return refs, nil
}
