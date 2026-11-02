package migrate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/ledger"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

var (
	// ErrClubNotFound — клуба нет в MySQL.
	ErrClubNotFound = errors.New("migrate: club not found in MySQL")
	// ErrBlocked — найдены блокеры: ничего не записано (транзакция откатена).
	ErrBlocked = errors.New("migrate: blockers found, nothing written")
	// ErrAlreadyMigrated — клуб уже есть в Postgres: повторный apply ничего не делает.
	ErrAlreadyMigrated = errors.New("migrate: club already exists in Postgres")
	// ErrVerifyMismatch — сверка нашла расхождения (в apply — транзакция откатена).
	ErrVerifyMismatch = errors.New("migrate: verification mismatch")
)

// PGQuerier — доступ расширения к Postgres: пул (plan, verify) или транзакция переноса (apply).
type PGQuerier = pgQuerier

// Env — окружение расширения (M2): снимок MySQL, Postgres, клуб, отчёт.
type Env struct {
	ClubID   int64
	ClubSlug string
	// Source — соединение MySQL в том же снимке, что и ядро.
	Source *sql.Conn
	// Target — транзакция переноса (apply: app.club_id клуба, роль zafiro_platform, если доступна) или пул.
	Target  PGQuerier
	Options *Options
	Report  *Report
}

// Add — находка расширения в общем отчёте.
func (e *Env) Add(sev Severity, code, tableName, column, value, sample, message string) {
	e.Report.add(sev, code, tableName, column, value, sample, message)
}

// Extension — точка расширения для производных данных (M2: JSON v2, tariff_sales, income_kind,
// session_purchases.zone_id/source, токены и ключи API, секреты APP_KEY, файлы). Вызывается после ядра:
// Plan — в сухом прогоне, Apply — в той же транзакции до сверки и коммита, Verify — в сверке.
type Extension interface {
	Name() string
	Plan(ctx context.Context, env *Env) error
	Apply(ctx context.Context, env *Env) error
	Verify(ctx context.Context, env *Env) error
}

// run — состояние одного прогона.
type run struct {
	o     *Options
	src   *source
	pg    pgQuerier
	write bool
	club  club
	rep   *Report

	userIDs  []int64 // аккаунты клуба (MySQL)
	planIDs  []int64 // тарифы платформы клуба (MySQL)
	balances []openingBalance
	packages []openingMinutes
	members  map[int64]bool
}

type openingBalance struct {
	UserID      int64
	Main, Bonus money.Tiyn
}

type openingMinutes struct {
	ID, UserID, TariffID, Minutes int64
}

func start(ctx context.Context, mode string, my *sql.DB, o *Options) (*run, error) {
	o.normalize()
	src, err := openSource(ctx, my)
	if err != nil {
		return nil, err
	}
	c, err := src.resolveClub(ctx, o.Club)
	if err != nil {
		src.Close()
		return nil, err
	}
	rep := newReport(mode)
	rep.ClubID, rep.ClubSlug, rep.ClubName = c.ID, c.Slug, c.Name
	return &run{o: o, src: src, club: c, rep: rep, members: map[int64]bool{}}, nil
}

// Plan — сухой прогон: читает клуб, преобразует все строки (как apply), проверяет данные и конфликты с
// Postgres, ничего не пишет. Блокеры — Report.Count(Blocker) > 0.
func Plan(ctx context.Context, my *sql.DB, pg *pgxpool.Pool, o Options) (*Report, error) {
	r, err := start(ctx, "plan", my, &o)
	if err != nil {
		return nil, err
	}
	defer r.src.Close()
	r.pg = pg
	if err := r.sourceInfo(ctx); err != nil {
		return r.rep, err
	}
	if err := r.preflight(ctx); err != nil {
		return r.rep, err
	}
	if err := r.copyAll(ctx); err != nil {
		return r.rep, err
	}
	if err := r.partitionsPlan(ctx); err != nil {
		return r.rep, err
	}
	for _, ext := range o.Extensions {
		if err := ext.Plan(ctx, r.env()); err != nil {
			return r.rep, fmt.Errorf("extension %s: %w", ext.Name(), err)
		}
	}
	return r.rep, nil
}

// Apply переносит клуб одной транзакцией Postgres: таблицы, входящие остатки (ledger), аудит, расширения,
// сдвиг последовательностей, сверка; любая ошибка, блокер или расхождение — откат, ничего не записано.
// Клуб уже есть в Postgres — ErrAlreadyMigrated (повторный запуск безопасен: ничего не меняет).
// pg — логин владельца схемы (или с членством в zafiro_platform и правом на последовательности): данные пишутся
// под SET LOCAL ROLE zafiro_platform (PD-9), setval — исходной ролью.
func Apply(ctx context.Context, my *sql.DB, pg *pgxpool.Pool, o Options) (*Report, error) {
	r, err := start(ctx, "apply", my, &o)
	if err != nil {
		return nil, err
	}
	defer r.src.Close()
	r.pg = pg
	if err := r.sourceInfo(ctx); err != nil {
		return r.rep, err
	}
	if err := r.alreadyMigrated(ctx); err != nil {
		return r.rep, err
	}
	if r.rep.Has(Blocker, CodeAlreadyMigrated) {
		return r.rep, ErrAlreadyMigrated
	}
	// Партиции — до транзакции данных (DDL функцией zafiro_create_monthly_partitions, идемпотентно).
	if err := r.ensurePartitions(ctx, pg); err != nil {
		return r.rep, err
	}

	err = pgx.BeginTxFunc(ctx, pg, pgx.TxOptions{}, func(tx pgx.Tx) error {
		r.pg, r.write = tx, true
		// Переносы клубов — по одному: аккаунты общие (users), их слияние не должно гоняться.
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('zafirogo:migrate-from-mysql'))"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "SELECT set_config('app.club_id', $1, true)", strconv.FormatInt(r.club.ID, 10)); err != nil {
			return err
		}
		asPlatform, err := setPlatformRole(ctx, tx)
		if err != nil {
			return err
		}
		if !asPlatform {
			slog.WarnContext(ctx, "migrate: login is not a member of zafiro_platform, writing as the login itself")
		}
		if err := r.preflight(ctx); err != nil { // под блокировкой: клуб мог появиться
			return err
		}
		if r.rep.Has(Blocker, CodeAlreadyMigrated) {
			return ErrAlreadyMigrated
		}
		if err := r.copyAll(ctx); err != nil {
			return err
		}
		if r.rep.Count(Blocker) > 0 {
			return ErrBlocked
		}
		if err := r.postOpenings(ctx, tx); err != nil {
			return err
		}
		for _, ext := range o.Extensions {
			if err := ext.Apply(ctx, r.env()); err != nil {
				return fmt.Errorf("extension %s: %w", ext.Name(), err)
			}
		}
		if r.rep.Count(Blocker) > 0 {
			return ErrBlocked
		}
		if asPlatform {
			if _, err := tx.Exec(ctx, "RESET ROLE"); err != nil {
				return err
			}
		}
		if err := r.shiftSequences(ctx, tx); err != nil {
			return err
		}
		// Сверка до коммита: расхождение — откат.
		if err := r.compare(ctx); err != nil {
			return err
		}
		if r.rep.Has(Blocker, CodeVerifyMismatch) {
			return ErrVerifyMismatch
		}
		return nil
	})
	return r.rep, err
}

// Verify сверяет перенесённый клуб с MySQL: число строк по таблицам, суммы балансов (основной, бонусный),
// total_spent, остатки склада, остаток кассы открытой смены, число ПК, согласованность ledger.
// Расхождение — ErrVerifyMismatch. Сверяет состояние на момент переноса: после начала работы клуба
// в zafirogo балансы и касса ожидаемо расходятся.
func Verify(ctx context.Context, my *sql.DB, pg *pgxpool.Pool, o Options) (*Report, error) {
	r, err := start(ctx, "verify", my, &o)
	if err != nil {
		return nil, err
	}
	defer r.src.Close()
	err = pgx.BeginTxFunc(ctx, pg, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		r.pg = tx
		if _, err := tx.Exec(ctx, "SELECT set_config('app.club_id', $1, true)", strconv.FormatInt(r.club.ID, 10)); err != nil {
			return err
		}
		if _, err := setPlatformRole(ctx, tx); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM clubs WHERE id = $1)", r.club.ID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			r.rep.add(Blocker, CodeVerifyMismatch, "clubs", "", "", strconv.FormatInt(r.club.ID, 10), "клуб не перенесён")
			return nil
		}
		return r.compare(ctx)
	})
	if err != nil {
		return r.rep, err
	}
	if r.rep.Count(Blocker) > 0 {
		return r.rep, ErrVerifyMismatch
	}
	return r.rep, nil
}

func (r *run) env() *Env {
	return &Env{ClubID: r.club.ID, ClubSlug: r.club.Slug, Source: r.src.conn, Target: r.pg, Options: r.o, Report: r.rep}
}

// setPlatformRole — SET LOCAL ROLE zafiro_platform, если логин в ней состоит (PD-9: data-миграции под ролью
// платформы, без обхода RLS суперпользователем).
func setPlatformRole(ctx context.Context, tx pgx.Tx) (bool, error) {
	var member bool
	if err := tx.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'zafiro_platform') AND pg_has_role(current_user, 'zafiro_platform', 'MEMBER')").
		Scan(&member); err != nil {
		return false, err
	}
	if !member {
		return false, nil
	}
	_, err := tx.Exec(ctx, "SET LOCAL ROLE zafiro_platform")
	return err == nil, err
}

// sourceInfo — параметры источника в отчёт (часовые пояса MySQL — для проверки правила времени).
func (r *run) sourceInfo(ctx context.Context) error {
	var version, global, session, system string
	if err := r.src.conn.QueryRowContext(ctx, "SELECT VERSION(), @@global.time_zone, @@session.time_zone, @@system_time_zone").
		Scan(&version, &global, &session, &system); err != nil {
		return fmt.Errorf("mysql: source info: %w", err)
	}
	r.rep.add(Info, CodeSource, "", "", "", "", fmt.Sprintf("MySQL %s, time_zone global=%s session=%s system=%s; строки времени читаются как %s",
		version, global, session, system, r.o.SourceTZ))
	return nil
}

// preflight — конфликты клуба с Postgres: клуб уже перенесён, slug занят (Q-025).
func (r *run) preflight(ctx context.Context) error {
	if err := r.alreadyMigrated(ctx); err != nil || r.rep.Has(Blocker, CodeAlreadyMigrated) {
		return err
	}
	return r.slugConflicts(ctx)
}

func (r *run) alreadyMigrated(ctx context.Context) error {
	if r.rep.Has(Blocker, CodeAlreadyMigrated) {
		return nil
	}
	var exists bool
	if err := r.pg.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM clubs WHERE id = $1)", r.club.ID).Scan(&exists); err != nil {
		return fmt.Errorf("pg: club: %w", err)
	}
	if exists {
		r.rep.add(Blocker, CodeAlreadyMigrated, "clubs", "", "", strconv.FormatInt(r.club.ID, 10),
			"клуб уже есть в zafirogo: повторный перенос не выполняется (сверка — verify)")
	}
	return nil
}

func (r *run) slugConflicts(ctx context.Context) error {
	rows, err := r.pg.Query(ctx, "SELECT id, slug FROM clubs WHERE lower(slug) = lower($1) AND id <> $2", r.club.Slug, r.club.ID)
	if err != nil {
		return fmt.Errorf("pg: club slug: %w", err)
	}
	type idSlug struct {
		ID   int64
		Slug string
	}
	taken, err := pgx.CollectRows(rows, pgx.RowToStructByPos[idSlug])
	if err != nil {
		return err
	}
	for _, c := range taken {
		r.rep.add(Blocker, CodeCaseConflict, "clubs", "slug", c.Slug, strconv.FormatInt(c.ID, 10),
			"slug клуба совпадает (без учёта регистра) со slug другого клуба zafirogo")
	}
	return nil
}

// copyAll проходит все таблицы ядра; в apply — пишет, в plan — только проверяет.
func (r *run) copyAll(ctx context.Context) error {
	tables := coreTables()
	for _, t := range tables {
		if t.Name == "users" {
			where, args := usersWhere(tables, r.o)
			t.Where = func(*Options) (string, []any) { return where, args }
			if err := r.danglingUsers(ctx, where, args); err != nil {
				return err
			}
		}
		if err := r.copyTable(ctx, t); err != nil {
			return err
		}
	}
	return r.skippedInfo(ctx)
}

// danglingUsers — ссылки на аккаунты, которых нет в MySQL (FK в старой схеме не везде): блокер.
func (r *run) danglingUsers(ctx context.Context, where string, args []any) error {
	sub := strings.TrimSuffix(strings.TrimPrefix(where, "id IN ("), ")")
	ids, err := r.src.strings1(ctx, "SELECT x.id FROM ("+sub+") AS x(id) WHERE x.id NOT IN (SELECT id FROM users)",
		bindClub(args, r.club.ID)...)
	if err != nil {
		return fmt.Errorf("mysql: dangling users: %w", err)
	}
	for _, id := range ids {
		r.rep.add(Blocker, CodeDanglingUser, "users", "id", "", id, "строки клуба ссылаются на несуществующий аккаунт")
	}
	return nil
}

func (r *run) skippedInfo(ctx context.Context) error {
	for _, s := range skippedTables {
		n, err := r.src.count(ctx, s.Name, s.Where, []any{r.club.ID})
		if err != nil {
			return err
		}
		if n > 0 {
			r.rep.add(Info, CodeSkippedTable, s.Name, "", "", "", fmt.Sprintf("не переносится ядром: %s (%d строк)", s.Reason, n))
		}
	}
	old, err := r.src.count(ctx, "api_request_logs", "club_id = ? AND created_at < ?", []any{r.club.ID, r.o.mysqlTime(r.o.retentionCutoff())})
	if err != nil {
		return err
	}
	if old > 0 {
		r.rep.add(Info, CodeRetention, "api_request_logs", "", "", "",
			fmt.Sprintf("старше срока хранения %s — не переносятся (%d строк)", r.o.APILogRetention, old))
	}
	return nil
}

// copyTable читает строки клуба, преобразует, проверяет и (в apply) вставляет пачками.
func (r *run) copyTable(ctx context.Context, t table) error {
	myCols, err := r.src.columns(ctx, t.Name)
	if err != nil {
		return err
	}
	pgCols, err := pgColumns(ctx, r.pg, t.Name)
	if err != nil {
		return err
	}
	cols, unmapped, err := planColumns(t, myCols, pgCols)
	if err != nil {
		r.rep.add(Blocker, CodeUnmappedColumn, t.Name, "", "", "", err.Error())
		return nil
	}
	stat := r.rep.table(t.Name)
	stat.Converted = map[string]string{}
	for _, c := range cols {
		if c.Kind != kindText {
			stat.Converted[c.Name] = c.Kind.String()
		}
	}
	for name := range t.Omit {
		stat.Converted[name] = "не переносится"
	}
	for _, name := range unmapped {
		r.rep.add(Blocker, CodeUnmappedColumn, t.Name, name, "", "", "колонка MySQL без пары в Postgres (схемы разошлись)")
	}
	idx := make(map[string]int, len(myCols))
	for i, c := range myCols {
		idx[c.Name] = i
	}
	where, args := t.Where(r.o)
	args = bindClub(args, r.club.ID)

	var batch [][]*string
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := r.flush(ctx, t, cols, batch, stat)
		batch = batch[:0]
		return err
	}
	err = r.src.scan(ctx, t.Name, myCols, where, t.OrderBy, args, func(row []*string) error {
		stat.Source++
		rowID := ""
		if i, ok := idx["id"]; ok && row[i] != nil {
			rowID = *row[i]
		}
		if err := r.transform(t, row, idx, rowID); err != nil {
			return err
		}
		out := make([]*string, len(cols))
		for i := range cols {
			c := &cols[i]
			raw := row[c.Index]
			v, err := c.convert(raw, r.o.SourceTZ)
			if err != nil {
				r.rep.add(Blocker, CodeBadValue, t.Name, c.Name, deref(raw), rowID, err.Error())
				continue
			}
			if v == nil && c.PG.NotNull {
				r.rep.add(Blocker, CodeNullNotNull, t.Name, c.Name, "", rowID, "NULL в колонке NOT NULL Postgres")
			}
			if v != nil && c.PG.Enum != nil && !c.PG.Enum[*v] {
				r.rep.add(Blocker, CodeEnumOutsideCheck, t.Name, c.Name, *v, rowID, "значение вне CHECK Postgres")
			}
			out[i] = v
		}
		batch = append(batch, out)
		if len(batch) >= r.o.BatchSize {
			return flush()
		}
		return nil
	})
	if err != nil {
		return err
	}
	return flush()
}

func deref(s *string) string {
	if s == nil {
		return "NULL"
	}
	return *s
}

func orEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func colIndex(cols []column, name string) int {
	for i, c := range cols {
		if c.Name == name {
			return i
		}
	}
	return -1
}

// flush — проверки пачки против Postgres (платформенные строки) и вставка (apply, без блокеров).
func (r *run) flush(ctx context.Context, t table, cols []column, batch [][]*string, stat *TableStat) error {
	if t.Platform {
		var err error
		if batch, err = r.mergePlatform(ctx, t, cols, batch); err != nil {
			return err
		}
	}
	if !r.write || len(batch) == 0 || r.rep.Count(Blocker) > 0 {
		return nil
	}
	arrays := make([]any, len(cols))
	for i := range cols {
		arr := make([]*string, len(batch))
		for j, row := range batch {
			arr[j] = row[i]
		}
		arrays[i] = arr
	}
	tag, err := r.pg.Exec(ctx, insertSQL(t.Name, cols, nil), arrays...)
	if err != nil {
		return fmt.Errorf("pg: insert %s: %w", t.Name, err)
	}
	stat.Written += tag.RowsAffected()
	return nil
}

// mergePlatform — строки платформенных таблиц, уже существующие в Postgres (общий аккаунт другого перенесённого
// клуба, тариф платформы), не вставляются; конфликты (тот же id — другой человек, телефон занят) — блокеры.
func (r *run) mergePlatform(ctx context.Context, t table, cols []column, batch [][]*string) ([][]*string, error) {
	idCol := colIndex(cols, "id")
	idList := make([]int64, 0, len(batch))
	for _, row := range batch {
		id, _ := strconv.ParseInt(deref(row[idCol]), 10, 64)
		idList = append(idList, id)
	}
	switch t.Name {
	case "users":
		r.userIDs = append(r.userIDs, idList...)
		return r.mergeUsers(ctx, cols, batch, idList)
	case "saas_plans":
		r.planIDs = append(r.planIDs, idList...)
		return r.mergeByKey(ctx, t.Name, cols, batch, idList, "slug", CodePlanConflict)
	}
	existing, err := existingIDs(ctx, r.pg, t.Name, idList)
	if err != nil {
		return nil, err
	}
	keep := batch[:0]
	for i, row := range batch {
		if !existing[idList[i]] {
			keep = append(keep, row)
		}
	}
	return keep, nil
}

func existingIDs(ctx context.Context, q pgQuerier, tableName string, ids []int64) (map[int64]bool, error) {
	rows, err := q.Query(ctx, fmt.Sprintf("SELECT id FROM %s WHERE id = ANY($1)", pgx.Identifier{tableName}.Sanitize()), ids)
	if err != nil {
		return nil, fmt.Errorf("pg: existing %s: %w", tableName, err)
	}
	list, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	out := make(map[int64]bool, len(list))
	for _, id := range list {
		out[id] = true
	}
	return out, nil
}

// mergeUsers — общий аккаунт (users — платформенная таблица): тот же id уже в zafirogo (перенесён с другим
// клубом) — не дублируется и не перезаписывается (после первого переноса источник правды — zafirogo);
// тот же id с другим телефоном или телефон, занятый другим id, — блокер (ручное слияние).
func (r *run) mergeUsers(ctx context.Context, cols []column, batch [][]*string, ids []int64) ([][]*string, error) {
	iPhone, iName, iEmail, iPassword := colIndex(cols, "phone"), colIndex(cols, "name"), colIndex(cols, "email"), colIndex(cols, "password")
	rows, err := r.pg.Query(ctx, "SELECT id, phone, name, COALESCE(email, ''), password FROM users WHERE id = ANY($1)", ids)
	if err != nil {
		return nil, fmt.Errorf("pg: users: %w", err)
	}
	type pgUser struct {
		ID                           int64
		Phone, Name, Email, Password string
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[pgUser])
	if err != nil {
		return nil, err
	}
	existing := make(map[int64]pgUser, len(list))
	for _, u := range list {
		existing[u.ID] = u
	}
	keep := batch[:0]
	var newPhones []string
	var newIDs []int64
	for i, row := range batch {
		id, phone := ids[i], deref(row[iPhone])
		sample := strconv.FormatInt(id, 10)
		if u, ok := existing[id]; ok {
			switch {
			case !strings.EqualFold(u.Phone, phone):
				r.rep.add(Blocker, CodeUserConflict, "users", "id", "", sample,
					"id аккаунта в zafirogo занят аккаунтом с другим телефоном")
			case u.Name != orEmpty(row[iName]) || u.Email != orEmpty(row[iEmail]) || u.Password != orEmpty(row[iPassword]):
				r.rep.add(Warning, CodeUserKept, "users", "", "", sample,
					"общий аккаунт уже в zafirogo (перенесён с другим клубом), данные отличаются — оставлены данные zafirogo")
			default:
				r.rep.add(Info, CodeUserKept, "users", "", "", sample, "общий аккаунт уже в zafirogo (перенесён с другим клубом)")
			}
			continue
		}
		keep = append(keep, row)
		newPhones = append(newPhones, strings.ToLower(phone))
		newIDs = append(newIDs, id)
	}
	if len(newIDs) > 0 {
		rows, err := r.pg.Query(ctx, "SELECT id, phone FROM users WHERE lower(phone) = ANY($1) AND NOT (id = ANY($2))", newPhones, newIDs)
		if err != nil {
			return nil, fmt.Errorf("pg: user phones: %w", err)
		}
		type idPhone struct {
			ID    int64
			Phone string
		}
		taken, err := pgx.CollectRows(rows, pgx.RowToStructByPos[idPhone])
		if err != nil {
			return nil, err
		}
		for _, u := range taken {
			r.rep.add(Blocker, CodeUserConflict, "users", "phone", u.Phone, strconv.FormatInt(u.ID, 10),
				"телефон аккаунта клуба уже занят другим аккаунтом zafirogo (ручное слияние)")
		}
	}
	return keep, nil
}

// mergeByKey — платформенный справочник: тот же id — тот же ключ (slug), иначе блокер; новый id — ключ свободен.
func (r *run) mergeByKey(ctx context.Context, tableName string, cols []column, batch [][]*string, ids []int64, key, code string) ([][]*string, error) {
	iKey := colIndex(cols, key)
	keys := make([]string, len(batch))
	for i, row := range batch {
		keys[i] = strings.ToLower(deref(row[iKey]))
	}
	tbl := pgx.Identifier{tableName}.Sanitize()
	k := pgx.Identifier{key}.Sanitize()
	rows, err := r.pg.Query(ctx, fmt.Sprintf("SELECT id, lower(%s) FROM %s WHERE id = ANY($1) OR lower(%s) = ANY($2)", k, tbl, k), ids, keys)
	if err != nil {
		return nil, fmt.Errorf("pg: %s: %w", tableName, err)
	}
	type idKey struct {
		ID  int64
		Key string
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[idKey])
	if err != nil {
		return nil, err
	}
	byID, byKey := map[int64]string{}, map[string]int64{}
	for _, e := range list {
		byID[e.ID], byKey[e.Key] = e.Key, e.ID
	}
	keep := batch[:0]
	for i, row := range batch {
		id, kv := ids[i], keys[i]
		if existingKey, ok := byID[id]; ok {
			if existingKey != kv {
				r.rep.add(Blocker, code, tableName, key, kv, strconv.FormatInt(id, 10), "id занят записью с другим ключом")
			}
			continue
		}
		if other, ok := byKey[kv]; ok {
			r.rep.add(Blocker, code, tableName, key, kv, strconv.FormatInt(other, 10), "ключ занят записью с другим id")
			continue
		}
		keep = append(keep, row)
	}
	return keep, nil
}

// transform — нормализация по решениям до преобразования типов (значения MySQL, текст).
func (r *run) transform(t table, row []*string, idx map[string]int, rowID string) error {
	get := func(name string) *string {
		if i, ok := idx[name]; ok {
			return row[i]
		}
		return nil
	}
	set := func(name, v string) { row[idx[name]] = &v }

	if i, ok := idx["created_at"]; ok && row[i] == nil {
		if t.Partitioned {
			// Партиционированная таблица требует created_at (как 0011: COALESCE(created_at, updated_at, now())).
			fill := r.o.mysqlTime(r.o.Now)
			if u := get("updated_at"); u != nil {
				fill = *u
			}
			set("created_at", fill)
			r.rep.add(Warning, CodeNullCreatedAt, t.Name, "created_at", "", rowID, "created_at = NULL → updated_at или момент переноса")
		} else {
			r.rep.add(Warning, CodeNullCreatedAt, t.Name, "created_at", "", rowID,
				"created_at = NULL (переносится как есть; NOT NULL DEFAULT now() — после проверки, Q-030)")
		}
	}

	switch t.Name {
	case "clubs":
		return r.checkClubSettings(row, idx, set)
	case "club_memberships":
		// Q-027: не-сотруднические роли → client (ClubRoles::label() показывал их «Клиент»).
		role := deref(get("role"))
		if !permissions.IsStaff(role) && role != permissions.RoleClient {
			r.rep.add(Warning, CodeOldRole, t.Name, "role", role, rowID, "старая роль участника → client")
			set("role", permissions.RoleClient)
		}
		if uid, err := strconv.ParseInt(deref(get("user_id")), 10, 64); err == nil {
			r.members[uid] = true
		}
	case "member_balances":
		uid, _ := strconv.ParseInt(deref(get("user_id")), 10, 64)
		ob := openingBalance{UserID: uid}
		for _, f := range []struct {
			col string
			dst *money.Tiyn
		}{{"balance", &ob.Main}, {"bonus_balance", &ob.Bonus}} {
			v, err := tiynFromDecimal(deref(get(f.col)))
			if err != nil {
				r.rep.add(Blocker, CodeBadValue, t.Name, f.col, deref(get(f.col)), rowID, err.Error())
				continue
			}
			*f.dst = v
			if v < 0 {
				r.rep.add(Warning, CodeNegativeBalance, t.Name, f.col, v.Tenge(), rowID,
					"отрицательный баланс — входящим остатком migration_opening_balance с пометкой (Q-004)")
			}
		}
		r.balances = append(r.balances, ob)
	case "time_packages":
		p := openingMinutes{}
		p.ID, _ = strconv.ParseInt(rowID, 10, 64)
		p.UserID, _ = strconv.ParseInt(deref(get("user_id")), 10, 64)
		p.TariffID, _ = strconv.ParseInt(deref(get("tariff_id")), 10, 64)
		p.Minutes, _ = strconv.ParseInt(deref(get("minutes_left")), 10, 64)
		if p.Minutes > 0 {
			r.packages = append(r.packages, p)
		}
	}
	return nil
}

// postOpenings — входящие остатки в ledger: балансы клиентов (billing.MigrateOpeningBalance, Q-004 — в том числе
// отрицательные), минуты пакетов, касса (остаток открытой смены или последней закрытой).
func (r *run) postOpenings(ctx context.Context, tx pgx.Tx) error {
	q := store.New(tx)
	var main, bonus money.Tiyn
	ops := 0
	for _, b := range r.balances {
		res, err := billing.MigrateOpeningBalance(ctx, q, r.club.ID, b.UserID, b.Main, b.Bonus)
		if err != nil {
			return fmt.Errorf("opening balance of user %d: %w", b.UserID, err)
		}
		if res.TransactionID != 0 {
			ops++
		}
		main += b.Main
		bonus += b.Bonus
	}
	for _, p := range r.packages {
		if !r.members[p.UserID] {
			r.rep.add(Warning, CodeMinutesNotMember, "time_packages", "user_id", "", strconv.FormatInt(p.ID, 10),
				"владелец пакета не участник клуба — входящий остаток минут в ledger не записан (пакет перенесён)")
			continue
		}
		if _, err := ledger.Post(ctx, q, ledger.Operation{
			ClubID: r.club.ID, Kind: ledger.KindMigrationOpeningBalance,
			IdempotencyKey: "opening_minutes:" + strconv.FormatInt(p.ID, 10),
			Minutes:        []ledger.MinuteEntry{{UserID: p.UserID, TimePackageID: p.ID, TariffID: p.TariffID, Minutes: p.Minutes}},
		}); err != nil {
			return fmt.Errorf("opening minutes of package %d: %w", p.ID, err)
		}
	}
	drawer, err := r.expectedDrawer(ctx)
	if err != nil {
		return err
	}
	if drawer.Amount != 0 {
		// Касса — актив: остаток = −SUM(проводок cash_drawer) (0002_ledger.sql); встречный счёт — opening_balance.
		if _, err := ledger.Post(ctx, q, ledger.Operation{
			ClubID: r.club.ID, Kind: ledger.KindMigrationOpeningBalance, IdempotencyKey: "opening_cash_drawer",
			Entries: []ledger.Entry{
				{Account: ledger.ClubAccount(ledger.AccountCashDrawer), Amount: -drawer.Amount},
				{Account: ledger.ClubAccount(ledger.AccountOpeningBalance), Amount: drawer.Amount},
			},
			Refs: ledger.Refs{CashShiftID: drawer.ShiftID},
		}); err != nil {
			return fmt.Errorf("opening cash drawer: %w", err)
		}
	}
	after, err := json.Marshal(map[string]any{
		"source": "mysql", "opening_operations": ops, "main_tiyn": int64(main), "bonus_tiyn": int64(bonus),
		"cash_drawer_tiyn": int64(drawer.Amount), "minute_packages": len(r.packages),
	})
	if err != nil {
		return err
	}
	subject := "club"
	if err := q.InsertAuditLog(ctx, store.InsertAuditLogParams{
		ClubID: &r.club.ID, ActorKind: "system", Category: "money", Action: "migration.club_imported",
		SubjectType: &subject, SubjectID: &r.club.ID, After: after,
	}); err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	r.rep.Totals["ledger: входящих остатков клиентов"] = strconv.Itoa(ops)
	return nil
}

// drawerState — ожидаемый остаток кассы.
type drawerState struct {
	Amount  money.Tiyn
	ShiftID int64 // открытая смена (0 — нет)
}

// expectedDrawer — остаток кассы по MySQL: открытая смена — opening_balance + приходы − расходы смены
// (CashRegisterService::totals()); нет открытой — closing_balance последней закрытой (касса между сменами).
func (r *run) expectedDrawer(ctx context.Context) (drawerState, error) {
	var shiftID int64
	var opening string
	err := r.src.conn.QueryRowContext(ctx,
		"SELECT id, opening_balance FROM cash_shifts WHERE club_id = ? AND status = 'open' ORDER BY id DESC LIMIT 1", r.club.ID).
		Scan(&shiftID, &opening)
	if errors.Is(err, sql.ErrNoRows) {
		var closing sql.NullString
		err := r.src.conn.QueryRowContext(ctx,
			"SELECT closing_balance FROM cash_shifts WHERE club_id = ? AND status = 'closed' ORDER BY closed_at DESC, id DESC LIMIT 1", r.club.ID).
			Scan(&closing)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && !closing.Valid) {
			return drawerState{}, nil
		}
		if err != nil {
			return drawerState{}, fmt.Errorf("mysql: last shift: %w", err)
		}
		amount, err := tiynFromDecimal(closing.String)
		return drawerState{Amount: amount}, err
	}
	if err != nil {
		return drawerState{}, fmt.Errorf("mysql: open shift: %w", err)
	}
	total, err := tiynFromDecimal(opening)
	if err != nil {
		return drawerState{}, err
	}
	rows, err := r.src.conn.QueryContext(ctx,
		"SELECT type, COALESCE(SUM(amount), 0) FROM cash_transactions WHERE club_id = ? AND cash_shift_id = ? GROUP BY type", r.club.ID, shiftID)
	if err != nil {
		return drawerState{}, fmt.Errorf("mysql: shift totals: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var typ, sum string
		if err := rows.Scan(&typ, &sum); err != nil {
			return drawerState{}, err
		}
		v, err := tiynFromDecimal(sum)
		if err != nil {
			return drawerState{}, err
		}
		switch typ {
		case "income":
			total += v
		case "expense":
			total -= v
		}
	}
	return drawerState{Amount: total, ShiftID: shiftID}, rows.Err()
}

// earliestMonth — первый месяц, под который нужны партиции журналов клуба.
func (r *run) earliestMonth(ctx context.Context) (time.Time, error) {
	earliest := r.o.Now
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"SELECT MIN(COALESCE(created_at, updated_at)) FROM activity_logs WHERE club_id = ?", []any{r.club.ID}},
		{"SELECT MIN(created_at) FROM api_request_logs WHERE club_id = ? AND created_at >= ?",
			[]any{r.club.ID, r.o.mysqlTime(r.o.retentionCutoff())}},
	} {
		var v sql.NullString
		if err := r.src.conn.QueryRowContext(ctx, q.sql, q.args...).Scan(&v); err != nil {
			return time.Time{}, fmt.Errorf("mysql: earliest log: %w", err)
		}
		if !v.Valid {
			continue
		}
		t, err := parseMySQLTime(v.String, r.o.SourceTZ)
		if err != nil {
			return time.Time{}, err
		}
		if t.Before(earliest) {
			earliest = t
		}
	}
	e := earliest.UTC()
	return time.Date(e.Year(), e.Month(), 1, 0, 0, 0, 0, time.UTC), nil
}

// monthsBetween — число месяцев [from, to] включительно.
func monthsBetween(from, to time.Time) int {
	return (to.Year()-from.Year())*12 + int(to.Month()) - int(from.Month()) + 1
}

func (r *run) partitionsPlan(ctx context.Context) error {
	from, err := r.earliestMonth(ctx)
	if err != nil {
		return err
	}
	r.rep.add(Info, "partitions", "", "", "", "",
		fmt.Sprintf("партиции журналов: с %s по %s (db.EnsurePartitions)", from.Format("2006-01"), r.o.Now.AddDate(0, 1, 0).Format("2006-01")))
	return nil
}

// ensurePartitions — месячные партиции журналов (db.PartitionedTables) с первого месяца данных клуба до следующего
// месяца; db.EnsurePartitions принимает до 36 месяцев за вызов.
func (r *run) ensurePartitions(ctx context.Context, pool *pgxpool.Pool) error {
	from, err := r.earliestMonth(ctx)
	if err != nil {
		return err
	}
	now := r.o.Now.UTC()
	last := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
	for m := from; !m.After(last); {
		n := min(monthsBetween(m, last), 36)
		if _, err := db.EnsurePartitions(ctx, pool, m, n); err != nil {
			return fmt.Errorf("partitions from %s: %w", m.Format("2006-01"), err)
		}
		m = m.AddDate(0, n, 0)
	}
	return nil
}

// shiftSequences — Q-029: id сохранены; последовательности сдвигаются выше максимального id таблицы во всей
// MySQL (не только клуба: Laravel продолжает выдавать id остальным клубам) плюс запас IDReserve; не уменьшаются.
func (r *run) shiftSequences(ctx context.Context, tx pgx.Tx) error {
	var names []string
	for _, t := range coreTables() {
		if !t.NoID {
			names = append(names, t.Name)
		}
	}
	for _, s := range skippedTables {
		names = append(names, s.Name)
	}
	for _, name := range names {
		var seq *string
		if err := tx.QueryRow(ctx, "SELECT pg_get_serial_sequence($1, 'id')", "public."+name).Scan(&seq); err != nil {
			return fmt.Errorf("pg: sequence of %s: %w", name, err)
		}
		if seq == nil { // id без последовательности (agent_qr_logins — uuid)
			continue
		}
		maxID, err := r.src.maxID(ctx, name)
		if err != nil {
			return err
		}
		target := maxID + r.o.IDReserve
		if target < 1 {
			continue
		}
		if _, err := tx.Exec(ctx, `SELECT setval($1::regclass, $2::bigint, true)
			WHERE $2::bigint > (SELECT COALESCE(s.last_value, s.start_value - 1) FROM pg_sequences s
			                    WHERE format('%I.%I', s.schemaname, s.sequencename)::regclass = $1::regclass)`, *seq, target); err != nil {
			return fmt.Errorf("pg: setval %s: %w", name, err)
		}
	}
	return nil
}
