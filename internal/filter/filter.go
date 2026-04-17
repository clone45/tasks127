package filter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type SearchParams struct {
	Where          map[string]any `json:"where"`
	OrderBy        []OrderClause  `json:"order_by"`
	Limit          int            `json:"limit"`
	Offset         int            `json:"offset"`
	IncludeDeleted bool           `json:"$include_deleted"`
}

type OrderClause struct {
	Field string `json:"field"`
	Dir   string `json:"dir"`
}

type FieldSpec struct {
	Column string
}

type Result struct {
	WhereClause string
	Args        []any
	OrderClause string
	Limit       int
	Offset      int
}

func ParseRequest(r *http.Request) (SearchParams, error) {
	var p SearchParams
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		return p, fmt.Errorf("invalid JSON: %w", err)
	}
	return p, nil
}

func Build(params SearchParams, allowed map[string]FieldSpec) (*Result, error) {
	whereSQL := "1=1"
	var args []any

	if len(params.Where) > 0 {
		w, a, err := buildWhere(params.Where, allowed)
		if err != nil {
			return nil, err
		}
		whereSQL = w
		args = a
	}

	if !params.IncludeDeleted {
		whereSQL = "(" + whereSQL + ") AND deleted_at IS NULL"
	}

	orderSQL, err := buildOrder(params.OrderBy, allowed)
	if err != nil {
		return nil, err
	}

	limit := params.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	offset := params.Offset
	if offset < 0 {
		offset = 0
	}

	return &Result{
		WhereClause: whereSQL,
		Args:        args,
		OrderClause: orderSQL,
		Limit:       limit,
		Offset:      offset,
	}, nil
}

func buildOrder(clauses []OrderClause, allowed map[string]FieldSpec) (string, error) {
	if len(clauses) == 0 {
		return " ORDER BY created_at DESC, id DESC", nil
	}

	var parts []string
	for _, c := range clauses {
		spec, ok := allowed[c.Field]
		if !ok {
			return "", fmt.Errorf("cannot order by %q: this field is not in the resource's allowlist of orderable fields. "+
				"Each resource exposes a fixed list of filterable and orderable fields; see docs/api.md for the list per resource", c.Field)
		}
		dir := "ASC"
		if strings.EqualFold(c.Dir, "desc") {
			dir = "DESC"
		}
		parts = append(parts, spec.Column+" "+dir)
	}
	return " ORDER BY " + strings.Join(parts, ", "), nil
}

func buildWhere(where map[string]any, allowed map[string]FieldSpec) (string, []any, error) {
	var clauses []string
	var args []any

	for key, val := range where {
		if strings.HasPrefix(key, "$") {
			switch key {
			case "$and":
				sql, a, err := buildCombinator(val, allowed, " AND ")
				if err != nil {
					return "", nil, err
				}
				clauses = append(clauses, sql)
				args = append(args, a...)

			case "$or":
				sql, a, err := buildCombinator(val, allowed, " OR ")
				if err != nil {
					return "", nil, err
				}
				clauses = append(clauses, sql)
				args = append(args, a...)

			default:
				return "", nil, fmt.Errorf("unknown top-level directive %q. Only $and and $or are supported as grouping directives "+
					"(shape: {\"$or\":[{...},{...}]}); every other top-level key must be a field name from the resource's filterable-fields allowlist", key)
			}
			continue
		}

		spec, ok := allowed[key]
		if !ok {
			return "", nil, fmt.Errorf("unknown field %q. Each resource has a fixed allowlist of filterable fields; "+
				"see docs/api.md for the list per resource. If the field you want exists on the resource but is not accepted by the filter DSL, "+
				"that is deliberate (some columns are not indexed for filtering)", key)
		}

		sql, a, err := buildFieldCondition(spec.Column, val)
		if err != nil {
			return "", nil, fmt.Errorf("field %s: %w", key, err)
		}
		clauses = append(clauses, sql)
		args = append(args, a...)
	}

	if len(clauses) == 0 {
		return "1=1", nil, nil
	}
	return strings.Join(clauses, " AND "), args, nil
}

func buildCombinator(val any, allowed map[string]FieldSpec, joiner string) (string, []any, error) {
	arr, ok := val.([]any)
	if !ok {
		return "", nil, fmt.Errorf("$and/$or must be an array of filter objects. Shape: {\"$or\":[{\"field1\":\"value\"},{\"field2\":{\"gte\":42}}]}")
	}

	var parts []string
	var args []any
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			return "", nil, fmt.Errorf("each element of $and/$or must be a filter object, not a string, number, or array. " +
				"Shape: {\"$or\":[{\"field1\":\"value\"},{\"field2\":{\"gte\":42}}]}")
		}
		sql, a, err := buildWhere(m, allowed)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, "("+sql+")")
		args = append(args, a...)
	}

	return "(" + strings.Join(parts, joiner) + ")", args, nil
}

func buildFieldCondition(column string, val any) (string, []any, error) {
	switch v := val.(type) {
	case string:
		return column + " = ?", []any{v}, nil
	case float64:
		return column + " = ?", []any{v}, nil
	case bool:
		return column + " = ?", []any{v}, nil
	case nil:
		return column + " IS NULL", nil, nil
	case map[string]any:
		return buildOperators(column, v)
	default:
		return "", nil, fmt.Errorf("unsupported value type %T for a field predicate: expected a scalar (string, number, bool, null) "+
			"for equality, or an operator object like {\"gte\":10} or {\"in\":[\"a\",\"b\"]}. Supported operators: "+
			"eq, ne, gt, gte, lt, lte, in, nin, contains, is_null", val)
	}
}

func buildOperators(column string, ops map[string]any) (string, []any, error) {
	var clauses []string
	var args []any

	for op, val := range ops {
		switch op {
		case "eq":
			if val == nil {
				clauses = append(clauses, column+" IS NULL")
			} else {
				clauses = append(clauses, column+" = ?")
				args = append(args, val)
			}

		case "ne":
			if val == nil {
				clauses = append(clauses, column+" IS NOT NULL")
			} else {
				clauses = append(clauses, column+" != ?")
				args = append(args, val)
			}

		case "gt":
			clauses = append(clauses, column+" > ?")
			args = append(args, val)

		case "gte":
			clauses = append(clauses, column+" >= ?")
			args = append(args, val)

		case "lt":
			clauses = append(clauses, column+" < ?")
			args = append(args, val)

		case "lte":
			clauses = append(clauses, column+" <= ?")
			args = append(args, val)

		case "in":
			sql, a, err := buildIn(column, val, false)
			if err != nil {
				return "", nil, err
			}
			clauses = append(clauses, sql)
			args = append(args, a...)

		case "nin":
			sql, a, err := buildIn(column, val, true)
			if err != nil {
				return "", nil, err
			}
			clauses = append(clauses, sql)
			args = append(args, a...)

		case "contains":
			s, ok := val.(string)
			if !ok {
				return "", nil, fmt.Errorf("contains requires a string value (case-insensitive substring match). " +
					"Shape: {\"field\":{\"contains\":\"substring\"}}")
			}
			escaped := strings.NewReplacer("%", "\\%", "_", "\\_").Replace(s)
			clauses = append(clauses, "LOWER("+column+") LIKE ? ESCAPE '\\'")
			args = append(args, "%"+strings.ToLower(escaped)+"%")

		case "is_null":
			b, ok := val.(bool)
			if !ok {
				return "", nil, fmt.Errorf("is_null requires a boolean: true to match NULL, false to match NOT NULL. " +
					"Shape: {\"field\":{\"is_null\":true}}")
			}
			if b {
				clauses = append(clauses, column+" IS NULL")
			} else {
				clauses = append(clauses, column+" IS NOT NULL")
			}

		default:
			return "", nil, fmt.Errorf("unknown operator %q. Supported operators: eq, ne, gt, gte, lt, lte, in, nin, contains, is_null. "+
				"For OR/AND grouping use the top-level $or or $and combinators. See docs/api.md 'The filter language' for the full syntax", op)
		}
	}

	if len(clauses) == 0 {
		return "1=1", nil, nil
	}
	return strings.Join(clauses, " AND "), args, nil
}

func buildIn(column string, val any, negate bool) (string, []any, error) {
	arr, ok := val.([]any)
	if !ok {
		return "", nil, fmt.Errorf("in/nin requires an array of values. Shape: {\"field\":{\"in\":[\"value1\",\"value2\"]}}. " +
			"An empty array is accepted: in matches nothing, nin matches everything")
	}
	if len(arr) == 0 {
		if negate {
			return "1=1", nil, nil
		}
		return "0=1", nil, nil
	}

	placeholders := make([]string, len(arr))
	for i := range arr {
		placeholders[i] = "?"
	}

	keyword := "IN"
	if negate {
		keyword = "NOT IN"
	}
	return column + " " + keyword + " (" + strings.Join(placeholders, ", ") + ")", arr, nil
}
