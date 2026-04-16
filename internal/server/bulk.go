package server

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/clone45/tasks127/internal/filter"
)

type bulkResult struct {
	Affected int64    `json:"affected"`
	IDs      []string `json:"ids"`
}

type bulkUpdateRequest struct {
	Where map[string]any `json:"where"`
	Set   map[string]any `json:"set"`
}

type bulkDeleteRequest struct {
	Where map[string]any `json:"where"`
}

func buildBulkSet(set map[string]any, settable map[string]bool) (string, []any, error) {
	if len(set) == 0 {
		return "", nil, fmt.Errorf("set must contain at least one field")
	}

	var clauses []string
	var args []any
	for field, val := range set {
		if !settable[field] {
			return "", nil, fmt.Errorf("field %q cannot be set in bulk update", field)
		}
		if val == nil {
			clauses = append(clauses, field+" = NULL")
		} else {
			clauses = append(clauses, field+" = ?")
			args = append(args, val)
		}
	}
	return strings.Join(clauses, ", "), args, nil
}

func (s *Server) execBulkUpdate(ctx context.Context, table string, filterFields map[string]filter.FieldSpec, where map[string]any, setClause string, setArgs []any, scopeFrag string, scopeArgs []any) (*bulkResult, error) {
	q, err := filter.Build(filter.SearchParams{Where: where}, filterFields)
	if err != nil {
		return nil, err
	}
	if scopeFrag != "" {
		q.WhereClause = "(" + q.WhereClause + ") AND " + scopeFrag
		q.Args = append(q.Args, scopeArgs...)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	ids, err := collectIDs(ctx, tx, table, q.WhereClause, q.Args)
	if err != nil {
		return nil, err
	}

	allArgs := append(setArgs, q.Args...)
	res, err := tx.ExecContext(ctx,
		fmt.Sprintf("UPDATE %s SET %s WHERE %s", table, setClause, q.WhereClause),
		allArgs...)
	if err != nil {
		return nil, err
	}

	affected, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	result := &bulkResult{Affected: affected, IDs: capIDs(ids)}
	s.audit(ctx, "bulk_update", table, "", map[string]any{
		"where": where, "set_clause": setClause,
		"affected": affected, "ids": result.IDs,
	})
	s.fireEvents(ctx, table, "update", result.IDs)
	return result, nil
}

func (s *Server) execBulkDelete(ctx context.Context, table string, filterFields map[string]filter.FieldSpec, where map[string]any, hasUpdatedAt bool, scopeFrag string, scopeArgs []any) (*bulkResult, error) {
	q, err := filter.Build(filter.SearchParams{Where: where}, filterFields)
	if err != nil {
		return nil, err
	}
	if scopeFrag != "" {
		q.WhereClause = "(" + q.WhereClause + ") AND " + scopeFrag
		q.Args = append(q.Args, scopeArgs...)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	ids, err := collectIDs(ctx, tx, table, q.WhereClause, q.Args)
	if err != nil {
		return nil, err
	}

	now := nowRFC3339()
	var setClause string
	var setArgs []any
	if hasUpdatedAt {
		setClause = "deleted_at = ?, updated_at = ?"
		setArgs = []any{now, now}
	} else {
		setClause = "deleted_at = ?"
		setArgs = []any{now}
	}

	allArgs := append(setArgs, q.Args...)
	res, err := tx.ExecContext(ctx,
		fmt.Sprintf("UPDATE %s SET %s WHERE %s", table, setClause, q.WhereClause),
		allArgs...)
	if err != nil {
		return nil, err
	}

	affected, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	result := &bulkResult{Affected: affected, IDs: capIDs(ids)}
	s.audit(ctx, "bulk_delete", table, "", map[string]any{
		"where": where, "affected": affected, "ids": result.IDs,
	})
	s.fireEvents(ctx, table, "delete", result.IDs)
	return result, nil
}

func collectIDs(ctx context.Context, tx *sql.Tx, table, whereClause string, args []any) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		fmt.Sprintf("SELECT id FROM %s WHERE %s LIMIT 1001", table, whereClause),
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func capIDs(ids []string) []string {
	if ids == nil {
		return []string{}
	}
	if len(ids) > 1000 {
		return ids[:1000]
	}
	return ids
}
