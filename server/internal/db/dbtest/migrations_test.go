//go:build integration

package dbtest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestParseMigration(t *testing.T) {
	m := ParseMigration(`-- +goose NO TRANSACTION
-- комментарий файла
-- +goose Up
-- индекс для отчёта
CREATE INDEX CONCURRENTLY IF NOT EXISTS t_a_index ON t (a);
CREATE INDEX CONCURRENTLY IF NOT EXISTS t_b_index
    ON t (b);
-- +goose StatementBegin
CREATE FUNCTION f() RETURNS int LANGUAGE sql AS $$
    SELECT 1;
$$;
-- +goose StatementEnd

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS t_b_index;
DROP INDEX CONCURRENTLY IF EXISTS t_a_index;
`)
	if !m.NoTransaction || !m.Up.NoTransaction || !m.Down.NoTransaction {
		t.Fatalf("NO TRANSACTION не распознан: %+v", m)
	}
	wantUp := []string{
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS t_a_index ON t (a);",
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS t_b_index\n    ON t (b);",
		"CREATE FUNCTION f() RETURNS int LANGUAGE sql AS $$\n    SELECT 1;\n$$;",
	}
	if strings.Join(m.Up.Statements, "|") != strings.Join(wantUp, "|") {
		t.Errorf("Up: %q", m.Up.Statements)
	}
	if len(m.Down.Statements) != 2 || !strings.Contains(m.Down.Statements[0], "t_b_index") {
		t.Errorf("Down: %q", m.Down.Statements)
	}
	if strings.Contains(m.Up.SQL, "+goose") || !strings.Contains(m.Up.SQL, "SELECT 1;") {
		t.Errorf("Up.SQL: %q", m.Up.SQL)
	}
	// Все миграции репозитория разбираются: у каждой есть Up и Down.
	files, _ := filepath.Glob(filepath.Join(MigrationsDir(), "*.sql"))
	for _, f := range files {
		body, err := os.ReadFile(f) // #nosec G304 -- файлы миграций репозитория
		if err != nil {
			t.Fatal(err)
		}
		m := ParseMigration(string(body))
		if len(m.Up.Statements) == 0 || len(m.Down.Statements) == 0 {
			t.Errorf("%s: пустая секция Up или Down", filepath.Base(f))
		}
	}
}

// Файл NO TRANSACTION с CREATE INDEX CONCURRENTLY применяется (аудит DB-3: раньше Migrate выполнял файл одной строкой —
// «CREATE INDEX CONCURRENTLY cannot run inside a transaction block»).
func TestMigrateNoTransaction(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("0001_t.sql", "-- +goose Up\nCREATE TABLE t (id int PRIMARY KEY, a int, b int);\n-- +goose Down\nDROP TABLE t;\n")
	write("0002_t_index.sql", "-- +goose NO TRANSACTION\n-- +goose Up\n"+
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS t_a_index ON t (a);\n"+
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS t_b_index ON t (b);\n"+
		"-- +goose Down\nDROP INDEX CONCURRENTLY IF EXISTS t_b_index;\nDROP INDEX CONCURRENTLY IF EXISTS t_a_index;\n")
	if err := MigrateDir(ctx, pool, dir); err != nil {
		t.Fatalf("MigrateDir: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		WHERE c.relname IN ('t_a_index', 't_b_index') AND i.indisvalid`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("индексы CONCURRENTLY: %d валидных, err %v", n, err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "0002_t_index.sql")) // #nosec G304 -- временный файл теста
	if err := ExecSection(ctx, pool, ParseMigration(string(body)).Down); err != nil {
		t.Fatalf("Down NO TRANSACTION: %v", err)
	}
}

func freshPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	admin := os.Getenv("ZAFIRO_TEST_DATABASE_URL")
	if admin == "" {
		var stop func()
		var err error
		if admin, stop, err = StartServer(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(stop)
	}
	dsn, err := FreshDatabase(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// ---------------------------------------------------------------------------
// Правила миграций на живой базе (docs/schema-changes.md §19.1, аудит DB-3)
// ---------------------------------------------------------------------------

// LiveRulesFrom — первая миграция, к которой применяются правила §19.1 (0045 и раньше — уже применены на dev/staging
// и не переписываются).
const LiveRulesFrom = 46

var (
	reCreateTable = regexp.MustCompile(`(?is)\bCREATE\s+(?:UNLOGGED\s+)?TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z0-9_."]+)`)
	reCreateIndex = regexp.MustCompile(`(?is)\bCREATE\s+(?:UNIQUE\s+)?INDEX\s+(CONCURRENTLY\s+)?(IF\s+NOT\s+EXISTS\s+)?[a-z0-9_"]*\s*ON\s+(ONLY\s+)?([a-z0-9_."]+)`)
	reDropIndex   = regexp.MustCompile(`(?is)\bDROP\s+INDEX\s+(CONCURRENTLY\s+)?(IF\s+EXISTS\s+)?`)
	reAlterTable  = regexp.MustCompile(`(?is)\bALTER\s+TABLE\s+(?:ONLY\s+)?(?:IF\s+EXISTS\s+)?([a-z0-9_."]+)`)
	reAddCheckFK  = regexp.MustCompile(`(?is)\bADD\s+(?:CONSTRAINT\s+[a-z0-9_"]+\s+)?(CHECK|FOREIGN\s+KEY)\b`)
)

func tableName(s string) string {
	s = strings.ToLower(strings.Trim(s, `"`))
	return strings.TrimPrefix(s, "public.")
}

// liveRuleViolations — нарушения §19.1 в секции Up файла миграции. Таблица, созданная в том же файле, пуста — для неё
// правила не нужны.
func liveRuleViolations(body string) []string {
	m := ParseMigration(body)
	created := map[string]bool{}
	for _, st := range m.Up.Statements {
		if sm := reCreateTable.FindStringSubmatch(st); sm != nil {
			created[tableName(sm[1])] = true
		}
	}
	var out []string
	for _, st := range m.Up.Statements {
		short := strings.Join(strings.Fields(st), " ")
		if len(short) > 90 {
			short = short[:90] + "…"
		}
		if sm := reCreateIndex.FindStringSubmatch(st); sm != nil && !created[tableName(sm[4])] && sm[3] == "" {
			switch {
			case sm[1] == "":
				out = append(out, "CREATE INDEX без CONCURRENTLY на существующей таблице (§19.1 п. 2): "+short)
			case !m.NoTransaction:
				out = append(out, "CONCURRENTLY без «-- +goose NO TRANSACTION» (§19.1 п. 2): "+short)
			case sm[2] == "":
				out = append(out, "CREATE INDEX CONCURRENTLY без IF NOT EXISTS (§19.1 п. 2): "+short)
			}
		}
		if sm := reDropIndex.FindStringSubmatch(st); sm != nil {
			if sm[1] == "" {
				out = append(out, "DROP INDEX без CONCURRENTLY (§19.1 п. 2): "+short)
			} else if !m.NoTransaction {
				out = append(out, "CONCURRENTLY без «-- +goose NO TRANSACTION» (§19.1 п. 2): "+short)
			}
		}
		if sm := reAlterTable.FindStringSubmatch(st); sm != nil && !created[tableName(sm[1])] &&
			reAddCheckFK.MatchString(st) && !regexp.MustCompile(`(?i)\bNOT\s+VALID\b`).MatchString(st) {
			out = append(out, "ADD CONSTRAINT CHECK/FOREIGN KEY без NOT VALID (§19.1 п. 1): "+short)
		}
	}
	return out
}

func TestLiveRuleViolations(t *testing.T) {
	bad := `-- +goose Up
ALTER TABLE ledger_transactions ADD CONSTRAINT k CHECK (kind IN ('a'));
CREATE INDEX ledger_x_index ON ledger_entries (club_id, created_at);
DROP INDEX ledger_y_index;
-- +goose Down
SELECT 1;
`
	if v := liveRuleViolations(bad); len(v) != 3 {
		t.Fatalf("ожидалось 3 нарушения, получено %d: %q", len(v), v)
	}
	noTx := "-- +goose Up\nCREATE INDEX CONCURRENTLY IF NOT EXISTS x ON sessions (club_id);\n-- +goose Down\nSELECT 1;\n"
	if v := liveRuleViolations(noTx); len(v) != 1 || !strings.Contains(v[0], "NO TRANSACTION") {
		t.Fatalf("CONCURRENTLY без NO TRANSACTION: %q", v)
	}
	good := `-- +goose NO TRANSACTION
-- +goose Up
CREATE INDEX CONCURRENTLY IF NOT EXISTS sessions_club_id_x_index ON sessions (club_id, x);
-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS sessions_club_id_x_index;
`
	if v := liveRuleViolations(good); len(v) != 0 {
		t.Fatalf("корректный файл: %q", v)
	}
	newTable := `-- +goose Up
CREATE TABLE things (id bigint PRIMARY KEY, club_id bigint NOT NULL);
CREATE INDEX things_club_id_index ON things (club_id);
ALTER TABLE things ADD CONSTRAINT things_club_fk FOREIGN KEY (club_id) REFERENCES clubs (id);
ALTER TABLE ledger_transactions ADD CONSTRAINT k2 CHECK (kind <> '') NOT VALID;
ALTER TABLE ledger_transactions VALIDATE CONSTRAINT k2;
-- +goose Down
DROP TABLE things;
`
	if v := liveRuleViolations(newTable); len(v) != 0 {
		t.Fatalf("новая таблица и NOT VALID: %q", v)
	}
}

// Миграции начиная с LiveRulesFrom соблюдают правила §19.1 (проверка CI, аудит DB-3).
func TestMigrationsFollowLiveRules(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(MigrationsDir(), "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("миграции: %v", err)
	}
	for _, f := range files {
		n, err := strconv.Atoi(strings.SplitN(filepath.Base(f), "_", 2)[0])
		if err != nil || n < LiveRulesFrom {
			continue
		}
		body, err := os.ReadFile(f) // #nosec G304 -- файлы миграций репозитория
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range liveRuleViolations(string(body)) {
			t.Errorf("%s: %s", filepath.Base(f), v)
		}
	}
}

// ---------------------------------------------------------------------------
// Откат миграций и права (аудит DB-2)
// ---------------------------------------------------------------------------

// aclText — ACL как отсортированный текст; ACL, равный правам по умолчанию владельца (acldefault), — пустая строка:
// после GRANT и REVOKE того же права relacl остаётся явным «owner=…/owner», хотя права те же, что у NULL.
func aclText(acl, kind, owner string) string {
	sorted := func(a string) string {
		return "(SELECT string_agg(x::text, ',' ORDER BY x::text) FROM unnest(" + a + ") x)"
	}
	def := "acldefault('" + kind + "', " + owner + ")"
	return "coalesce(nullif(" + sorted("coalesce("+acl+", "+def+")") + ", " + sorted(def) + "), '')"
}

// privSnapshot — права и действительность ограничений схемы: ACL таблиц/последовательностей/представлений,
// колонок, функций, схемы public, права по умолчанию, ограничения (NOT VALID), общие роли zafiro_*.
var privSnapshot = `
SELECT 'rel ' || c.relname, ` + aclText("c.relacl", "r", "c.relowner") + `
  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p', 'v', 'm', 'f')
UNION ALL
SELECT 'seq ' || c.relname, ` + aclText("c.relacl", "s", "c.relowner") + `
  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = 'public' AND c.relkind = 'S'
UNION ALL
SELECT 'col ' || c.relname || '.' || a.attname, (SELECT string_agg(x::text, ',' ORDER BY x::text) FROM unnest(a.attacl) x)
  FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = 'public' AND a.attacl IS NOT NULL AND cardinality(a.attacl) > 0 AND a.attnum > 0 AND NOT a.attisdropped
UNION ALL
SELECT 'fn ' || p.oid::regprocedure::text, ` + aclText("p.proacl", "f", "p.proowner") + `
  FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
 WHERE n.nspname = 'public' AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid = p.oid AND d.deptype = 'e')
UNION ALL
SELECT 'schema ' || n.nspname, ` + aclText("n.nspacl", "n", "n.nspowner") + `
  FROM pg_namespace n WHERE n.nspname = 'public'
UNION ALL
SELECT 'defacl ' || pg_get_userbyid(d.defaclrole) || ':' || coalesce(n.nspname, '') || ':' || d.defaclobjtype::text,
       (SELECT string_agg(x::text, ',' ORDER BY x::text) FROM unnest(d.defaclacl) x)
  FROM pg_default_acl d LEFT JOIN pg_namespace n ON n.oid = d.defaclnamespace
UNION ALL
SELECT 'con ' || c.conrelid::regclass::text || '.' || c.conname, CASE WHEN c.convalidated THEN 'valid' ELSE 'NOT VALID' END
  FROM pg_constraint c JOIN pg_namespace n ON n.oid = c.connamespace WHERE n.nspname = 'public'
UNION ALL
SELECT 'role ' || r.rolname, 'exists' FROM pg_roles r WHERE r.rolname IN ('zafiro_app', 'zafiro_platform')
ORDER BY 1`

func snapshot(ctx context.Context, t *testing.T, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	rows, err := pool.Query(ctx, privSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func diff(want, got map[string]string) []string {
	var out []string
	for k, v := range want {
		if g, ok := got[k]; !ok {
			out = append(out, "нет: "+k+" = "+v)
		} else if g != v {
			out = append(out, fmt.Sprintf("%s: %q, ожидалось %q", k, g, v))
		}
	}
	for k, v := range got {
		if _, ok := want[k]; !ok {
			out = append(out, "лишнее: "+k+" = "+v)
		}
	}
	sort.Strings(out)
	return out
}

// downResidue — известные расхождения «up-to N» и «up → down-to N» (Down старых миграций не переписываются;
// docs/schema-changes.md §19.3). Ключ — префикс строки расхождения; значение — миграция, чей Down их оставляет.
// Новое расхождение (не из списка) — ошибка: Down новой миграции должен вернуть права версии N.
var downResidue = map[string]int{
	// 0028: Down возвращает табличные права, колоночные GRANT INSERT/UPDATE(…) ON users остаются.
	"лишнее: col users.": 28,
	// 0013: Down возвращает проверку agent_certificates как NOT VALID (без VALIDATE).
	"con agent_certificates.agent_certificates_issued_via_check": 13,
}

func knownResidue(line string, version int) bool {
	for prefix, from := range downResidue {
		if strings.HasPrefix(line, prefix) && version < from {
			return true
		}
	}
	return false
}

// TestDownMigrationsRestorePrivileges: для каждой версии k «down k → up k» возвращает права версии k в точности
// (повторная выкладка после отката не теряет и не добавляет прав); «down-to N» отличается от «up-to N» только
// известными остатками downResidue; полный «down-to 0 → up» совпадает с исходным «up». Свой контейнер Postgres:
// Down 0004 удаляет общие для кластера роли zafiro_app/zafiro_platform (аудит DB-2).
func TestDownMigrationsRestorePrivileges(t *testing.T) {
	if testing.Short() {
		t.Skip("долгий: откат всех миграций")
	}
	ctx := context.Background()
	admin, stop, err := StartServer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	dsn, err := FreshDatabase(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	files, _ := filepath.Glob(filepath.Join(MigrationsDir(), "*.sql"))
	sort.Strings(files)
	type mig struct {
		version int
		name    string
		m       Migration
	}
	migs := make([]mig, 0, len(files))
	for _, f := range files {
		body, err := os.ReadFile(f) // #nosec G304 -- файлы миграций репозитория
		if err != nil {
			t.Fatal(err)
		}
		v, _ := strconv.Atoi(strings.SplitN(filepath.Base(f), "_", 2)[0])
		migs = append(migs, mig{v, filepath.Base(f), ParseMigration(string(body))})
	}
	exec := func(name string, sec Section) {
		t.Helper()
		if err := ExecSection(ctx, pool, sec); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}

	// up-to k для каждого k: эталон прав версии k.
	up := make([]map[string]string, len(migs)+1) // up[i] — после i применённых миграций
	up[0] = snapshot(ctx, t, pool)
	for i, m := range migs {
		exec(m.name+" Up", m.m.Up)
		up[i+1] = snapshot(ctx, t, pool)
	}

	// Откат по одной версии k: «down k → up k» возвращает права up-to k (кроме известных остатков Down более поздних
	// версий); затем down k и сравнение с up-to N — только известные остатки.
	for i := len(migs) - 1; i >= 0; i-- {
		m := migs[i]
		exec(m.name+" Down", m.m.Down)
		exec(m.name+" Up", m.m.Up)
		for _, line := range diff(up[i+1], snapshot(ctx, t, pool)) {
			if !knownResidue(line, m.version) { // остатки Down более поздних версий — известны и проверены ниже
				t.Errorf("down %s → up: права отличаются от up-to %d: %s", m.name, m.version, line)
			}
		}
		exec(m.name+" Down", m.m.Down)
		prev := 0
		if i > 0 {
			prev = migs[i-1].version
		}
		for _, line := range diff(up[i], snapshot(ctx, t, pool)) {
			if !knownResidue(line, prev) {
				t.Errorf("down-to %d (после Down %s): %s", prev, m.name, line)
			}
		}
	}

	// Повторная выкладка после полного отката: права как у исходного up.
	for _, m := range migs {
		exec(m.name+" Up (повторно)", m.m.Up)
	}
	if d := diff(up[len(migs)], snapshot(ctx, t, pool)); len(d) != 0 {
		t.Errorf("down-to 0 → up: права отличаются от исходного up:\n%s", strings.Join(d, "\n"))
	}
}
