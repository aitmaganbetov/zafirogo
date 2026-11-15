package db

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// optionalFilterRe — необязательный фильтр по параметру: ($x = ” OR …), ($x = 0 OR …), ($x IS NULL OR …).
var optionalFilterRe = regexp.MustCompile(`sqlc\.n?arg\(\w+\)(::\w+)?\s*(=\s*''|=\s*0|IS NULL)\s+OR\b`)

// PB-02: каждый запрос db/queries с необязательным фильтром выполняется с планом под значения (customPlanQueries),
// и в списке нет имён, которых нет среди запросов sqlc.
func TestCustomPlanQueriesCoverOptionalFilters(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "db", "queries", "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("db/queries: %v (%d files)", err, len(files))
	}
	var missing []string
	found := 0
	for _, f := range files {
		src, err := os.ReadFile(f) // #nosec G304 -- файлы репозитория
		if err != nil {
			t.Fatal(err)
		}
		for _, block := range strings.Split(string(src), "-- name: ")[1:] {
			name, _, _ := strings.Cut(block, " ")
			if !optionalFilterRe.MatchString(block) {
				continue
			}
			found++
			if _, ok := customPlanQueries[name]; !ok {
				missing = append(missing, filepath.Base(f)+": "+name)
			}
		}
	}
	if found < 25 {
		t.Fatalf("only %d queries with optional filters found — the pattern no longer matches db/queries", found)
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("запросы с необязательным фильтром без плана под значения — добавьте в db.customPlanQueries:\n%s", strings.Join(missing, "\n"))
	}
	methods := reflect.TypeFor[*store.Queries]()
	for name := range customPlanQueries {
		if _, ok := methods.MethodByName(name); !ok {
			t.Errorf("customPlanQueries: запроса %s нет в store", name)
		}
	}
}

func TestCustomPlan(t *testing.T) {
	for sql, want := range map[string]bool{
		"-- name: CountActivityLogPage :one\nSELECT 1": true,
		"-- name: GetClub :one\nSELECT 1":              false,
		"SELECT set_config('app.club_id', $1, true)":   false,
		"-- name: CountActivityLogPageX :one\nSELECT":  false,
		"-- name: ": false,
	} {
		if got := CustomPlan(sql); got != want {
			t.Errorf("CustomPlan(%q) = %v, want %v", sql, got, want)
		}
	}
}
