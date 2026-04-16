package filter

import (
	"strings"
	"testing"
)

var testFields = map[string]FieldSpec{
	"id":         {Column: "id"},
	"name":       {Column: "name"},
	"status":     {Column: "status"},
	"team_id":    {Column: "team_id"},
	"created_at": {Column: "created_at"},
}

// mustBuild calls Build and fails the test if an error occurs.
func mustBuild(t *testing.T, p SearchParams) *Result {
	t.Helper()
	r, err := Build(p, testFields)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	return r
}

// normalized returns the where clause with leading/trailing whitespace trimmed
// and internal runs of whitespace collapsed to single spaces. Makes assertions
// resilient to formatting.
func normalized(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func TestBuild_EmptyWhereAddsSoftDeleteFilter(t *testing.T) {
	r := mustBuild(t, SearchParams{})
	got := normalized(r.WhereClause)
	want := "(1=1) AND deleted_at IS NULL"
	if got != want {
		t.Errorf("where: got %q, want %q", got, want)
	}
	if len(r.Args) != 0 {
		t.Errorf("args: got %v, want empty", r.Args)
	}
}

func TestBuild_IncludeDeletedSkipsSoftDeleteFilter(t *testing.T) {
	r := mustBuild(t, SearchParams{IncludeDeleted: true})
	if strings.Contains(r.WhereClause, "deleted_at") {
		t.Errorf("expected no deleted_at filter, got: %q", r.WhereClause)
	}
}

func TestBuild_ScalarSugar(t *testing.T) {
	r := mustBuild(t, SearchParams{
		Where: map[string]any{"name": "Alice"},
	})
	if !strings.Contains(r.WhereClause, "name = ?") {
		t.Errorf("expected 'name = ?' in where, got: %q", r.WhereClause)
	}
	if len(r.Args) != 1 || r.Args[0] != "Alice" {
		t.Errorf("args: got %v, want [Alice]", r.Args)
	}
}

func TestBuild_Operators(t *testing.T) {
	tests := []struct {
		name    string
		where   map[string]any
		wantSQL string // substring expected in WHERE
		wantArg any
	}{
		{"eq explicit", map[string]any{"name": map[string]any{"eq": "A"}}, "name = ?", "A"},
		{"ne", map[string]any{"name": map[string]any{"ne": "A"}}, "name != ?", "A"},
		{"gt", map[string]any{"created_at": map[string]any{"gt": "2026"}}, "created_at > ?", "2026"},
		{"gte", map[string]any{"created_at": map[string]any{"gte": "2026"}}, "created_at >= ?", "2026"},
		{"lt", map[string]any{"created_at": map[string]any{"lt": "2026"}}, "created_at < ?", "2026"},
		{"lte", map[string]any{"created_at": map[string]any{"lte": "2026"}}, "created_at <= ?", "2026"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := mustBuild(t, SearchParams{Where: tc.where})
			if !strings.Contains(r.WhereClause, tc.wantSQL) {
				t.Errorf("where: got %q, want it to contain %q", r.WhereClause, tc.wantSQL)
			}
			if len(r.Args) != 1 || r.Args[0] != tc.wantArg {
				t.Errorf("args: got %v, want [%v]", r.Args, tc.wantArg)
			}
		})
	}
}

func TestBuild_In(t *testing.T) {
	r := mustBuild(t, SearchParams{
		Where: map[string]any{"status": map[string]any{"in": []any{"open", "done"}}},
	})
	if !strings.Contains(r.WhereClause, "status IN (?, ?)") {
		t.Errorf("where: got %q, want it to contain 'status IN (?, ?)'", r.WhereClause)
	}
	if len(r.Args) != 2 || r.Args[0] != "open" || r.Args[1] != "done" {
		t.Errorf("args: got %v, want [open done]", r.Args)
	}
}

func TestBuild_InEmpty(t *testing.T) {
	r := mustBuild(t, SearchParams{
		Where: map[string]any{"status": map[string]any{"in": []any{}}},
	})
	// Empty IN set can never match → 0=1
	if !strings.Contains(r.WhereClause, "0=1") {
		t.Errorf("empty 'in' should produce 0=1 (impossible), got: %q", r.WhereClause)
	}
}

func TestBuild_NinEmpty(t *testing.T) {
	r := mustBuild(t, SearchParams{
		Where: map[string]any{"status": map[string]any{"nin": []any{}}},
	})
	// Empty NIN should match everything → no restrictive clause (just soft-delete and 1=1)
	if strings.Contains(r.WhereClause, "0=1") {
		t.Errorf("empty 'nin' should NOT produce 0=1, got: %q", r.WhereClause)
	}
}

func TestBuild_Contains(t *testing.T) {
	r := mustBuild(t, SearchParams{
		Where: map[string]any{"name": map[string]any{"contains": "Ali"}},
	})
	if !strings.Contains(r.WhereClause, "LOWER(name) LIKE ? ESCAPE '\\'") {
		t.Errorf("contains: got where %q", r.WhereClause)
	}
	if len(r.Args) != 1 || r.Args[0] != "%ali%" {
		t.Errorf("args: got %v, want [%%ali%%]", r.Args)
	}
}

func TestBuild_ContainsEscapesWildcards(t *testing.T) {
	r := mustBuild(t, SearchParams{
		Where: map[string]any{"name": map[string]any{"contains": "50%_off"}},
	})
	arg := r.Args[0].(string)
	// Wildcards should be escaped so the user's literal % and _ don't match everything.
	if !strings.Contains(arg, `\%`) || !strings.Contains(arg, `\_`) {
		t.Errorf("contains should escape %% and _; got arg: %q", arg)
	}
}

func TestBuild_IsNull(t *testing.T) {
	r := mustBuild(t, SearchParams{
		Where: map[string]any{"team_id": map[string]any{"is_null": true}},
	})
	if !strings.Contains(r.WhereClause, "team_id IS NULL") {
		t.Errorf("is_null true: got %q", r.WhereClause)
	}

	r = mustBuild(t, SearchParams{
		Where: map[string]any{"team_id": map[string]any{"is_null": false}},
	})
	if !strings.Contains(r.WhereClause, "team_id IS NOT NULL") {
		t.Errorf("is_null false: got %q", r.WhereClause)
	}
}

func TestBuild_NullScalarIsIsNull(t *testing.T) {
	r := mustBuild(t, SearchParams{
		Where: map[string]any{"team_id": nil},
	})
	if !strings.Contains(r.WhereClause, "team_id IS NULL") {
		t.Errorf("null scalar should become IS NULL, got: %q", r.WhereClause)
	}
}

func TestBuild_AndOrCombinators(t *testing.T) {
	r := mustBuild(t, SearchParams{
		Where: map[string]any{
			"$or": []any{
				map[string]any{"status": "open"},
				map[string]any{"status": "done"},
			},
		},
	})
	if !strings.Contains(r.WhereClause, " OR ") {
		t.Errorf("expected OR in where, got: %q", r.WhereClause)
	}
	if len(r.Args) != 2 {
		t.Errorf("expected 2 args, got %v", r.Args)
	}
}

func TestBuild_UnknownField(t *testing.T) {
	_, err := Build(SearchParams{
		Where: map[string]any{"nonexistent": "x"},
	}, testFields)
	if err == nil {
		t.Fatal("expected error for unknown field, got nil")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error should mention field name, got: %v", err)
	}
}

func TestBuild_UnknownOperator(t *testing.T) {
	_, err := Build(SearchParams{
		Where: map[string]any{"name": map[string]any{"like": "x"}},
	}, testFields)
	if err == nil {
		t.Fatal("expected error for unknown operator, got nil")
	}
}

func TestBuild_UnknownDirective(t *testing.T) {
	_, err := Build(SearchParams{
		Where: map[string]any{"$bogus": []any{}},
	}, testFields)
	if err == nil {
		t.Fatal("expected error for unknown $directive, got nil")
	}
}

func TestBuild_LimitDefaultsAndCaps(t *testing.T) {
	r := mustBuild(t, SearchParams{})
	if r.Limit != 50 {
		t.Errorf("default limit: got %d, want 50", r.Limit)
	}

	r = mustBuild(t, SearchParams{Limit: 10_000})
	if r.Limit != 200 {
		t.Errorf("limit cap: got %d, want 200", r.Limit)
	}

	r = mustBuild(t, SearchParams{Limit: -5})
	if r.Limit != 50 {
		t.Errorf("negative limit should fall back to default, got %d", r.Limit)
	}
}

func TestBuild_OffsetDefault(t *testing.T) {
	r := mustBuild(t, SearchParams{Offset: -10})
	if r.Offset != 0 {
		t.Errorf("negative offset should become 0, got %d", r.Offset)
	}
}

func TestBuild_OrderByDefault(t *testing.T) {
	r := mustBuild(t, SearchParams{})
	if !strings.Contains(r.OrderClause, "created_at DESC") {
		t.Errorf("default order: got %q", r.OrderClause)
	}
}

func TestBuild_OrderByCustom(t *testing.T) {
	r := mustBuild(t, SearchParams{
		OrderBy: []OrderClause{{Field: "name", Dir: "asc"}},
	})
	if !strings.Contains(r.OrderClause, "name ASC") {
		t.Errorf("custom order: got %q", r.OrderClause)
	}
}

func TestBuild_OrderByUnknownFieldRejected(t *testing.T) {
	_, err := Build(SearchParams{
		OrderBy: []OrderClause{{Field: "secret", Dir: "asc"}},
	}, testFields)
	if err == nil {
		t.Fatal("expected error ordering by unknown field, got nil")
	}
}

func TestBuild_OrderByDirDefaultsToAsc(t *testing.T) {
	r := mustBuild(t, SearchParams{
		OrderBy: []OrderClause{{Field: "name", Dir: ""}},
	})
	if !strings.Contains(r.OrderClause, "name ASC") {
		t.Errorf("missing dir should default to ASC, got %q", r.OrderClause)
	}
}

func TestBuild_NestedCombinators(t *testing.T) {
	r := mustBuild(t, SearchParams{
		Where: map[string]any{
			"$or": []any{
				map[string]any{"status": "open"},
				map[string]any{
					"$and": []any{
						map[string]any{"status": "done"},
						map[string]any{"name": "Alice"},
					},
				},
			},
		},
	})
	// Should have both OR and AND in structure.
	if !strings.Contains(r.WhereClause, " OR ") || !strings.Contains(r.WhereClause, " AND ") {
		t.Errorf("nested combinators: got %q", r.WhereClause)
	}
	if len(r.Args) != 3 {
		t.Errorf("nested: got %d args, want 3 (open, done, Alice)", len(r.Args))
	}
}

func TestBuild_ImplicitAndMultipleFields(t *testing.T) {
	r := mustBuild(t, SearchParams{
		Where: map[string]any{
			"status":  "open",
			"team_id": "T",
		},
	})
	// Two conditions implicitly AND-ed
	if !strings.Contains(r.WhereClause, " AND ") {
		t.Errorf("implicit AND: got %q", r.WhereClause)
	}
	if len(r.Args) != 2 {
		t.Errorf("args: got %v, want 2", r.Args)
	}
}
