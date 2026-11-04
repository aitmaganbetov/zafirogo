package migrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Состояние перенесённого клуба (0027_club_migration_state.sql, S5-M3-1, S5-M3-2):
//
//	apply → imported (подписка закрыта, клуб не работает) → проверка (verify, отчёты) → go-live → live
//	                 └─ purge (удалить и перенести заново) — только до go-live
const (
	// StateImported — перенесён и не открыт: можно удалить (Purge) и перенести заново.
	StateImported = "imported"
	// StateLive — клуб работает в zafirogo; удалить его инструментом нельзя.
	StateLive = "live"
)

const (
	// CodeLifecycle — находки purge/go-live.
	CodeLifecycle          = "lifecycle"
	goLiveSubscriptionsKey = "go_live_subscriptions"
	clubImportedAction     = "migration.club_imported"
)

var (
	// ErrClubState — клуб не в том состоянии: purge клуба live, go-live клуба, который уже открыт.
	ErrClubState = errors.New("migrate: club is not in the required migration state")
	// ErrClubInUse — у клуба в состоянии imported уже есть операции в zafirogo (purge отклонён).
	ErrClubInUse = errors.New("migrate: club has operations in zafirogo")
	// ErrConfirm — подтверждение purge не совпало со slug клуба.
	ErrConfirm = errors.New("migrate: confirmation does not match the club slug")
	// ErrClubNotInPostgres — клуба нет в Postgres.
	ErrClubNotInPostgres = errors.New("migrate: club not found in Postgres")
)

type pgClub struct {
	ID         int64
	Slug, Name string
	State      string
}

// resolvePGClub — клуб zafirogo по id или slug (clubs — платформенная таблица без RLS).
func resolvePGClub(ctx context.Context, q pgQuerier, ref string) (pgClub, error) {
	sqlText := "SELECT id, slug, name, migration_state FROM clubs WHERE slug = $1 FOR UPDATE"
	arg := any(ref)
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil && id > 0 {
		sqlText, arg = "SELECT id, slug, name, migration_state FROM clubs WHERE id = $1 FOR UPDATE", id
	}
	var c pgClub
	err := q.QueryRow(ctx, sqlText, arg).Scan(&c.ID, &c.Slug, &c.Name, &c.State)
	if isNoRows(err) {
		return c, fmt.Errorf("%w: %q", ErrClubNotInPostgres, ref)
	}
	if err != nil {
		return c, fmt.Errorf("pg: club: %w", err)
	}
	return c, nil
}

func lifecycleTx(ctx context.Context, pg *pgxpool.Pool, rep *Report, ref string, fn func(tx pgx.Tx, c pgClub) error) error {
	return pgx.BeginTxFunc(ctx, pg, pgx.TxOptions{}, func(tx pgx.Tx) error {
		// Под той же блокировкой, что apply: перенос и удаление клуба не пересекаются (общие аккаунты).
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('zafirogo:migrate-from-mysql'))"); err != nil {
			return err
		}
		c, err := resolvePGClub(ctx, tx, ref)
		if err != nil {
			return err
		}
		rep.ClubID, rep.ClubSlug, rep.ClubName = c.ID, c.Slug, c.Name
		return fn(tx, c)
	})
}

// Purge удаляет перенесённый клуб из Postgres, чтобы перенести его заново (S5-M2-О2 → S5-M3-1). Только для клуба в
// состоянии imported без операций в zafirogo; confirm — slug клуба (защита от опечатки в id). Строки клуба во всех
// таблицах с club_id (включая только-дополняемые ledger и аудит) удаляет функция zafiro_purge_imported_club (0027,
// SECURITY DEFINER, EXECUTE только у владельца схемы) — pg должен быть логином владельца (ZAFIRO_MIGRATE_PG_DSN).
// Аккаунты (users), на которые ссылался только этот клуб, удаляются вместе с ним (иначе исправленный в MySQL телефон
// аккаунта дал бы при повторном переносе блокер «тот же id, другой телефон»); суперадмины и общие аккаунты остаются.
func Purge(ctx context.Context, pg *pgxpool.Pool, ref, confirm string) (*Report, error) {
	rep := newReport("purge")
	err := lifecycleTx(ctx, pg, rep, ref, func(tx pgx.Tx, c pgClub) error {
		if c.State != StateImported {
			rep.add(Blocker, CodeLifecycle, "clubs", "migration_state", c.State, strconv.FormatInt(c.ID, 10),
				"клуб открыт для работы (go-live): удаление запрещено — его деньги живут в ledger zafirogo")
			return ErrClubState
		}
		if confirm != c.Slug {
			rep.add(Blocker, CodeLifecycle, "clubs", "slug", confirm, strconv.FormatInt(c.ID, 10),
				"--confirm не совпадает со slug клуба")
			return ErrConfirm
		}
		users, err := purgeableUsers(ctx, tx, c.ID, rep)
		if err != nil {
			return err
		}
		var raw []byte
		err = tx.QueryRow(ctx, "SELECT zafiro_purge_imported_club($1, $2, $3)::text", c.ID, confirm, users).Scan(&raw)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "55000" {
				if strings.Contains(pgErr.Message, "operations") || strings.Contains(pgErr.Message, "audited") {
					rep.add(Blocker, CodeLifecycle, "", "", "", strconv.FormatInt(c.ID, 10),
						"у клуба уже есть операции в zafirogo (ledger/аудит): удаление запрещено")
					return ErrClubInUse
				}
				return ErrClubState
			}
			return fmt.Errorf("pg: purge: %w", err)
		}
		deleted := map[string]int64{}
		if err := json.Unmarshal(raw, &deleted); err != nil {
			return fmt.Errorf("pg: purge result: %w", err)
		}
		for _, name := range sortedKeys(deleted) {
			rep.table(name).Target = deleted[name]
		}
		rep.Totals["purge: удалено аккаунтов (только этого клуба)"] = strconv.FormatInt(deleted["users"], 10)
		rep.add(Info, CodeLifecycle, "clubs", "", "", strconv.FormatInt(c.ID, 10),
			"клуб удалён из zafirogo (событие migration.club_purged в журнале платформы): можно переносить заново")
		return nil
	})
	return rep, err
}

// userRef — внешний ключ на users.id.
type userRef struct {
	Table, Column string
	HasClub       bool
	FullView      bool // zafiro_platform видит все строки (нет RLS или политика USING (true))
}

// purgeableUsers — аккаунты, на которые ссылаются строки клуба и больше ничего: кандидаты собираются по всем внешним
// ключам на users из таблиц с club_id, затем блокируются (FOR UPDATE: новая ссылка — членство в другом клубе —
// ждёт конца транзакции и не пройдёт проверку FK) и проверяются под ролью zafiro_platform (видит все клубы) по ВСЕМ
// внешним ключам на users. Если какую-то таблицу со ссылками роль платформы видит не целиком, аккаунты не удаляются
// (иначе каскад FK мог бы задеть другой клуб).
func purgeableUsers(ctx context.Context, tx pgx.Tx, clubID int64, rep *Report) ([]int64, error) {
	refs, err := userRefs(ctx, tx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('app.club_id', $1, true)", strconv.FormatInt(clubID, 10)); err != nil {
		return nil, err
	}
	asPlatform, err := setPlatformRole(ctx, tx)
	if err != nil {
		return nil, err
	}
	if !asPlatform {
		rep.add(Warning, CodeLifecycle, "users", "", "", "", "логин не состоит в zafiro_platform: аккаунты клуба не удаляются")
		return nil, nil
	}
	set := map[int64]bool{}
	for _, ref := range refs {
		if !ref.HasClub {
			continue
		}
		ids, err := collectIDs(ctx, tx, fmt.Sprintf("SELECT DISTINCT %s FROM %s WHERE club_id = $1 AND %s IS NOT NULL",
			pgx.Identifier{ref.Column}.Sanitize(), ref.Table, pgx.Identifier{ref.Column}.Sanitize()), clubID)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			set[id] = true
		}
	}
	candidates := make([]int64, 0, len(set))
	for id := range set {
		candidates = append(candidates, id)
	}
	slices.Sort(candidates)
	if _, err := tx.Exec(ctx, "RESET ROLE"); err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	for _, ref := range refs {
		if !ref.FullView {
			rep.add(Warning, CodeLifecycle, ref.Table, ref.Column, "", "",
				"роль zafiro_platform видит таблицу не целиком: аккаунты клуба не удаляются (остаются в zafirogo)")
			return nil, nil
		}
	}
	if _, err := tx.Exec(ctx, "SELECT 1 FROM users WHERE id = ANY($1) ORDER BY id FOR UPDATE", candidates); err != nil {
		return nil, fmt.Errorf("pg: lock users: %w", err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE zafiro_platform"); err != nil {
		return nil, err
	}
	keep := map[int64]bool{}
	super, err := collectIDs(ctx, tx, "SELECT id FROM users WHERE id = ANY($1) AND is_superadmin", candidates)
	if err != nil {
		return nil, err
	}
	for _, id := range super {
		keep[id] = true
	}
	for _, ref := range refs {
		col := pgx.Identifier{ref.Column}.Sanitize()
		q := fmt.Sprintf("SELECT DISTINCT %s FROM %s WHERE %s = ANY($1)", col, ref.Table, col)
		args := []any{candidates}
		if ref.HasClub {
			q += " AND club_id IS DISTINCT FROM $2"
			args = append(args, clubID)
		}
		ids, err := collectIDs(ctx, tx, q, args...)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			keep[id] = true
		}
	}
	if _, err := tx.Exec(ctx, "RESET ROLE"); err != nil {
		return nil, err
	}
	out := candidates[:0]
	for _, id := range candidates {
		if !keep[id] {
			out = append(out, id)
		}
	}
	if n := len(candidates) - len(out); n > 0 {
		rep.Totals["purge: аккаунтов остаётся (другие клубы, суперадмины)"] = strconv.Itoa(n)
	}
	return out, nil
}

// userRefs — все внешние ключи на users.id (одна колонка) из таблиц public, кроме партиций.
func userRefs(ctx context.Context, q pgQuerier) ([]userRef, error) {
	rows, err := q.Query(ctx, `
		SELECT r.oid::regclass::text, a.attname,
		       EXISTS (SELECT 1 FROM pg_attribute ca WHERE ca.attrelid = r.oid AND ca.attname = 'club_id' AND NOT ca.attisdropped),
		       NOT r.relrowsecurity OR EXISTS (
		           SELECT 1 FROM pg_policy p
		           WHERE p.polrelid = r.oid AND p.polcmd IN ('*', 'r') AND p.polpermissive
		             AND (SELECT oid FROM pg_roles WHERE rolname = 'zafiro_platform') = ANY (p.polroles)
		             AND pg_get_expr(p.polqual, p.polrelid) = 'true')
		FROM pg_constraint c
		JOIN pg_class r ON r.oid = c.conrelid
		JOIN pg_namespace n ON n.oid = r.relnamespace AND n.nspname = 'public'
		JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = c.conkey[1]
		WHERE c.contype = 'f' AND c.confrelid = 'public.users'::regclass AND c.conparentid = 0
		  AND NOT r.relispartition AND cardinality(c.conkey) = 1
		ORDER BY 1, 2`)
	if err != nil {
		return nil, fmt.Errorf("pg: user references: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[userRef])
}

func collectIDs(ctx context.Context, q pgQuerier, sqlText string, args ...any) ([]int64, error) {
	rows, err := q.Query(ctx, sqlText, args...)
	if err != nil {
		return nil, fmt.Errorf("pg: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// GoLive открывает перенесённый клуб для работы (S5-M3-2): подписки — статус из записи аудита
// migration.club_imported (до заморозки или прежний), clubs.migration_state = live (окончательно: удалить клуб
// инструментом больше нельзя), запись аудита migration.club_live и событие журнала платформы. Выполнять после
// verify (код 0) и проверки клуба.
func GoLive(ctx context.Context, pg *pgxpool.Pool, ref string) (*Report, error) {
	rep := newReport("go-live")
	err := lifecycleTx(ctx, pg, rep, ref, func(tx pgx.Tx, c pgClub) error {
		if c.State != StateImported {
			rep.add(Blocker, CodeLifecycle, "clubs", "migration_state", c.State, strconv.FormatInt(c.ID, 10),
				"клуб уже открыт для работы")
			return ErrClubState
		}
		if _, err := tx.Exec(ctx, "SELECT set_config('app.club_id', $1, true)", strconv.FormatInt(c.ID, 10)); err != nil {
			return err
		}
		if _, err := setPlatformRole(ctx, tx); err != nil {
			return err
		}
		var after []byte
		err := tx.QueryRow(ctx, `SELECT after FROM audit_log WHERE club_id = $1 AND action = $2 ORDER BY created_at DESC, id DESC LIMIT 1`,
			c.ID, clubImportedAction).Scan(&after)
		if isNoRows(err) {
			rep.add(Blocker, CodeLifecycle, "audit_log", "", clubImportedAction, strconv.FormatInt(c.ID, 10),
				"нет записи аудита переноса: клуб создан не инструментом переноса?")
			return ErrClubState
		}
		if err != nil {
			return fmt.Errorf("pg: import audit: %w", err)
		}
		var imported struct {
			Subscriptions []subscriptionRestore `json:"go_live_subscriptions"`
		}
		if err := json.Unmarshal(after, &imported); err != nil {
			return fmt.Errorf("pg: import audit: %w", err)
		}
		for _, s := range imported.Subscriptions {
			tag, err := tx.Exec(ctx, `UPDATE club_subscriptions
				SET status = $3, canceled_at = CASE WHEN $4 THEN NULL ELSE canceled_at END, updated_at = now()
				WHERE club_id = $1 AND id = $2 AND status = 'suspended'`, c.ID, s.ID, s.Status, s.ClearCanceledAt)
			if err != nil {
				return fmt.Errorf("pg: subscription %d: %w", s.ID, err)
			}
			if tag.RowsAffected() == 0 {
				rep.add(Warning, CodeLifecycle, "club_subscriptions", "status", "", strconv.FormatInt(s.ID, 10),
					"подписка изменена после переноса (не suspended) — статус не трогаем")
				continue
			}
			rep.add(Info, CodeLifecycle, "club_subscriptions", "status", "suspended → "+s.Status, strconv.FormatInt(s.ID, 10),
				"подписка открыта")
		}
		if _, err := tx.Exec(ctx, "UPDATE clubs SET migration_state = $2 WHERE id = $1", c.ID, StateLive); err != nil {
			return fmt.Errorf("pg: club state: %w", err)
		}
		q := store.New(tx)
		subject := "club"
		payload, err := json.Marshal(map[string]any{"migration_state": StateLive, goLiveSubscriptionsKey: imported.Subscriptions})
		if err != nil {
			return err
		}
		if err := q.InsertAuditLog(ctx, store.InsertAuditLogParams{
			ClubID: &c.ID, ActorKind: "system", Category: "settings", Action: "migration.club_live",
			SubjectType: &subject, SubjectID: &c.ID, After: payload,
		}); err != nil {
			return fmt.Errorf("audit: %w", err)
		}
		if _, err := activity.RecordPlatform(ctx, q, activity.PlatformEntry{
			Event: "migration.club_live", Key: "Перенесённый клуб :club открыт для работы",
			Params: map[string]string{"club": c.Name}, ClubID: c.ID, SubjectType: `App\Models\Club`, SubjectID: c.ID,
		}); err != nil {
			return fmt.Errorf("platform log: %w", err)
		}
		rep.add(Info, CodeLifecycle, "clubs", "migration_state", StateImported+" → "+StateLive, strconv.FormatInt(c.ID, 10),
			"клуб открыт для работы; удаление (purge) больше невозможно")
		return nil
	})
	return rep, err
}
