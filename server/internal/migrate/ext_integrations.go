package migrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/webhook"
)

// Коды находок расширений M2.
const (
	CodeSecret      = "Q-050"  // секрет APP_KEY: нет ключа, не расшифровывается
	CodeMobileToken = "Q-058"  // токен мобильного приложения не переносится
	CodeJSONv2      = "Q-003"  // JSON v1 → v2
	CodeSettingsNrm = "Q-023n" // нормализация настроек (что изменено)
	CodeTariffSales = "Q-007"
	CodeIncomeKind  = "CSQ-2"
	CodeSessionZone = "SV-3"
	CodeReceiptID   = "CQ-9"
	CodeFile        = "Q-085" // файлы картинок
	CodePlatform    = "platform"
)

// laravelUserMorph — tokenable_type токенов пользователей Sanctum (morph class App\Models\User).
const laravelUserMorph = `App\Models\User`

// m2Tables — таблицы интеграций (M2): ключи API, вебхуки. Ссылки на аккаунты (created_by …) входят в набор
// переносимых аккаунтов (usersWhere): иначе ключ, выпущенный бывшим сотрудником, нарушил бы FK.
func m2Tables(e *integrations) []table {
	return []table{
		// Ключи API — как есть: хэш тот же (hash('sha256', $plain), H1-1), клиенты не перевыпускают ключи.
		{Name: "api_keys", Where: clubScope, OrderBy: "id", UserRefs: []string{"created_by", "revoked_by"}},
		// Ключи идемпотентности — с club_id (L-1, 0012): в MySQL его нет, ключ принадлежит клубу ключа API.
		{Name: "api_idempotency_keys", OrderBy: "id", PGWhere: "club_id = {club}",
			Where: func(*Options) (string, []any) {
				return "api_key_id IN (SELECT id FROM api_keys WHERE club_id = ?)", []any{clubArg}
			},
			Const: map[string]string{}}, // club_id подставляет integrations.tables()
		// Вебхуки: секрет — расшифровка APP_KEY и шифрование ключом данных (Q-050, контекст webhook.SecretContext).
		{Name: "webhook_endpoints", Where: clubScope, OrderBy: "id", UserRefs: []string{"created_by"},
			Transform: e.sealWebhookSecret},
		{Name: "webhook_deliveries", Where: clubScope, OrderBy: "id"},
	}
}

// userRefTablesM2 — ссылки таблиц M2 на аккаунты (для usersWhere).
func userRefTablesM2() []table { return m2Tables(&integrations{}) }

// integrations — расширение: ключи API, вебхуки (с секретами), токены мобильных приложений.
type integrations struct {
	env *Env
}

// Integrations — расширение M2 «интеграции»: api_keys, api_idempotency_keys, webhook_endpoints (секрет — Q-050),
// webhook_deliveries, personal_access_tokens → mobile_api_tokens (docs/mobile-compat.md §2.3).
func Integrations() Extension { return &integrations{} }

func (e *integrations) Name() string { return "integrations" }

// Tables — таблицы из skippedTables, которые переносит это расширение.
func (e *integrations) Tables() []string {
	return []string{"api_keys", "api_idempotency_keys", "webhook_endpoints", "webhook_deliveries"}
}

func (e *integrations) tables(env *Env) []table {
	e.env = env
	ts := m2Tables(e)
	for i := range ts {
		if ts[i].Const != nil {
			ts[i].Const = map[string]string{"club_id": strconv.FormatInt(env.ClubID, 10)}
		}
	}
	return ts
}

func (e *integrations) Plan(ctx context.Context, env *Env) error  { return e.copy(ctx, env) }
func (e *integrations) Apply(ctx context.Context, env *Env) error { return e.copy(ctx, env) }

func (e *integrations) copy(ctx context.Context, env *Env) error {
	for _, t := range e.tables(env) {
		if err := env.run.copyTable(ctx, t); err != nil {
			return err
		}
	}
	if err := e.keyPrefixes(ctx, env); err != nil {
		return err
	}
	return e.mobileTokens(ctx, env, false)
}

// keyPrefixes — префиксы действующих ключей API клуба в отчёт: по ним балансировщик направляет /api/v1 клуба на
// zafirogo (README: переключение клуба), пока остальные клубы на старом сервере. Префикс — не секрет (виден в
// интерфейсе и журнале запросов).
func (e *integrations) keyPrefixes(ctx context.Context, env *Env) error {
	list, err := env.run.src.strings1(ctx,
		"SELECT prefix FROM api_keys WHERE club_id = ? AND revoked_at IS NULL ORDER BY prefix", env.ClubID)
	if err != nil {
		return fmt.Errorf("mysql: api key prefixes: %w", err)
	}
	if len(list) > 0 {
		env.Report.Totals["nginx: префиксы ключей /api/v1"] = strings.Join(list, " ")
	}
	return nil
}

// sealWebhookSecret — секрет вебхука: Crypt (каст 'encrypted', без serialize) → secretbox с контекстом клуба.
// Значения не печатаются: в отчёте только id endpoint'а.
func (e *integrations) sealWebhookSecret(row []*string, idx map[string]int, rowID string) (bool, error) {
	env := e.env
	i := idx["secret"]
	if row[i] == nil {
		return true, nil
	}
	plain, ok := e.openLaravel(env, "webhook_endpoints", "secret", rowID, *row[i])
	if !ok {
		return true, nil
	}
	if env.Options.DataKeys == nil {
		env.Add(Blocker, CodeSecret, "webhook_endpoints", "secret", "", rowID,
			"нет ключей данных нового сервера (ZAFIRO_SECURITY_DATA_KEYS): секрет вебхука не зашифровать")
		return true, nil
	}
	sealed, err := env.Options.DataKeys.SealWith(plain, webhook.SecretContext(env.ClubID))
	if err != nil {
		return false, fmt.Errorf("seal webhook secret of endpoint %s: %w", rowID, err)
	}
	row[i] = &sealed
	return true, nil
}

// openLaravel расшифровывает значение старым APP_KEY; не вышло — блокер (ok = false).
func (e *integrations) openLaravel(env *Env, tableName, col, rowID, v string) (string, bool) {
	return openLaravel(env.Options.LaravelKeys, env.Report, tableName, col, rowID, v)
}

func openLaravel(keys *LaravelKeys, rep *Report, tableName, col, rowID, v string) (string, bool) {
	if keys == nil {
		rep.add(Blocker, CodeSecret, tableName, col, "", rowID,
			"секрет зашифрован APP_KEY старого Laravel: задайте ZAFIRO_MIGRATE_LARAVEL_APP_KEY")
		return "", false
	}
	plain, err := keys.DecryptString(v)
	if err != nil {
		msg := "секрет не расшифровывается APP_KEY (другой ключ — добавьте его в ZAFIRO_MIGRATE_LARAVEL_PREVIOUS_KEYS)"
		if errors.Is(err, ErrLaravelPayload) {
			msg = "значение не похоже на шифротекст Laravel (Crypt)"
		}
		rep.add(Blocker, CodeSecret, tableName, col, "", rowID, msg)
		return "", false
	}
	return plain, true
}

// Verify — число строк таблиц (MySQL = Postgres) и секреты вебхуков: открытый секрет из Postgres (ключ данных)
// совпадает с расшифрованным APP_KEY из MySQL; токены мобильных приложений — по id и хэшу.
func (e *integrations) Verify(ctx context.Context, env *Env) error {
	r := env.run
	for _, t := range e.tables(env) {
		where, args := t.Where(env.Options)
		my, err := r.src.count(ctx, t.Name, where, bindClub(args, env.ClubID))
		if err != nil {
			return err
		}
		cond := t.PGWhere
		if cond == "" {
			cond = "club_id = {club}"
		}
		cond, pgArgs := pgWhere(cond, env.ClubID, nil, nil)
		var pgCount int64
		if err := env.Target.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s WHERE %s", pgx.Identifier{t.Name}.Sanitize(), cond), pgArgs...).
			Scan(&pgCount); err != nil {
			return fmt.Errorf("pg: count %s: %w", t.Name, err)
		}
		stat := env.Report.table(t.Name)
		stat.Source, stat.Target = my, pgCount
		if my != pgCount {
			env.Add(Blocker, CodeVerifyMismatch, t.Name, "", "", "", fmt.Sprintf("строк: MySQL %d, Postgres %d", my, pgCount))
		}
	}
	if err := e.verifySecrets(ctx, env); err != nil {
		return err
	}
	return e.mobileTokens(ctx, env, true)
}

func (e *integrations) verifySecrets(ctx context.Context, env *Env) error {
	rows, err := env.Source.QueryContext(ctx, "SELECT id, secret FROM webhook_endpoints WHERE club_id = ? ORDER BY id", env.ClubID)
	if err != nil {
		return fmt.Errorf("mysql: webhook secrets: %w", err)
	}
	type sec struct {
		ID     int64
		Secret string
	}
	var mine []sec
	for rows.Next() {
		var s sec
		if err := rows.Scan(&s.ID, &s.Secret); err != nil {
			_ = rows.Close()
			return err
		}
		mine = append(mine, s)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(mine) == 0 {
		return nil
	}
	if env.Options.LaravelKeys == nil || env.Options.DataKeys == nil {
		env.Add(Warning, CodeSecret, "webhook_endpoints", "secret", "", "",
			"секреты вебхуков не сверены: нужны ZAFIRO_MIGRATE_LARAVEL_APP_KEY и ZAFIRO_SECURITY_DATA_KEYS")
		return nil
	}
	for _, s := range mine {
		id := strconv.FormatInt(s.ID, 10)
		var stored string
		err := env.Target.QueryRow(ctx, "SELECT secret FROM webhook_endpoints WHERE club_id = $1 AND id = $2", env.ClubID, s.ID).Scan(&stored)
		if errors.Is(err, pgx.ErrNoRows) {
			continue // число строк уже сверено
		}
		if err != nil {
			return fmt.Errorf("pg: webhook secret: %w", err)
		}
		want, err := env.Options.LaravelKeys.DecryptString(s.Secret)
		if err != nil {
			env.Add(Blocker, CodeVerifyMismatch, "webhook_endpoints", "secret", "", id, "секрет в MySQL не расшифровывается APP_KEY")
			continue
		}
		got, err := env.Options.DataKeys.OpenWith(stored, webhook.SecretContext(env.ClubID))
		if err != nil || got != want {
			env.Add(Blocker, CodeVerifyMismatch, "webhook_endpoints", "secret", "", id,
				"секрет вебхука в Postgres не совпадает с секретом старого сервера")
		}
	}
	env.Report.Totals["webhook_endpoints: секретов сверено"] = strconv.Itoa(len(mine))
	return nil
}

var reTokenHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

// mobileToken — строка personal_access_tokens пользователя для клуба.
type mobileToken struct {
	ID, UserID                       int64
	Name, Hash                       string
	CreatedAt, LastUsedAt, ExpiresAt *string // timestamptz (RFC 3339) или nil
}

// mobileTokens — токены Sanctum пользователей с ability "club:<id>" клуба (docs/mobile-compat.md §2.3): id тот же
// (приложение шлёт «<id>|<токен>»), хэш как есть, истёкшие не переносятся. verify — сверка вместо записи.
func (e *integrations) mobileTokens(ctx context.Context, env *Env, verify bool) error {
	r := env.run
	ability := "club:" + strconv.FormatInt(env.ClubID, 10)
	rows, err := env.Source.QueryContext(ctx, `SELECT id, tokenable_id, name, token, abilities, created_at, last_used_at, expires_at
		FROM personal_access_tokens
		WHERE tokenable_type = ? AND abilities LIKE ? AND (expires_at IS NULL OR expires_at > ?)
		ORDER BY id`, laravelUserMorph, `%"`+ability+`"%`, env.Options.mysqlTime(env.Options.Now))
	if err != nil {
		return fmt.Errorf("mysql: personal_access_tokens: %w", err)
	}
	var list []mobileToken
	for rows.Next() {
		var t mobileToken
		var abilities, created, used, expires *string
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &t.Hash, &abilities, &created, &used, &expires); err != nil {
			_ = rows.Close()
			return err
		}
		id := strconv.FormatInt(t.ID, 10)
		var abs []string
		if abilities == nil || json.Unmarshal([]byte(*abilities), &abs) != nil || !containsString(abs, ability) {
			continue // LIKE нашёл подстроку, но ability другая
		}
		if !reTokenHash.MatchString(t.Hash) {
			env.Add(Warning, CodeMobileToken, "personal_access_tokens", "token", "", id, "хэш токена не sha256 hex — не переносится")
			continue
		}
		if utf8.RuneCountInString(t.Name) > 255 {
			t.Name = string([]rune(t.Name)[:255])
			env.Add(Warning, CodeMobileToken, "personal_access_tokens", "name", "", id, "имя устройства длиннее 255 — обрезано")
		}
		for _, p := range []struct {
			src *string
			dst **string
		}{{created, &t.CreatedAt}, {used, &t.LastUsedAt}, {expires, &t.ExpiresAt}} {
			if p.src == nil {
				continue
			}
			v, err := (&column{Kind: kindTime}).convert(p.src, env.Options.SourceTZ)
			if err != nil {
				env.Add(Blocker, CodeBadValue, "personal_access_tokens", "", *p.src, id, err.Error())
				continue
			}
			*p.dst = v
		}
		list = append(list, t)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// Владелец токена — аккаунт, перенесённый с клубом (или раньше): иначе токен не переносится.
	userIDs := make([]int64, 0, len(list))
	for _, t := range list {
		userIDs = append(userIDs, t.UserID)
	}
	known, err := r.knownUsers(ctx, userIDs)
	if err != nil {
		return err
	}
	keep := list[:0]
	for _, t := range list {
		if !known[t.UserID] {
			if !verify {
				env.Add(Warning, CodeMobileToken, "personal_access_tokens", "tokenable_id", "", strconv.FormatInt(t.ID, 10),
					"владелец токена не перенесён с клубом — токен не переносится (вход заново)")
			}
			continue
		}
		keep = append(keep, t)
	}
	list = keep
	stat := env.Report.table("mobile_api_tokens")
	stat.Source = int64(len(list))
	if verify {
		return e.verifyTokens(ctx, env, list, stat)
	}
	if !r.write || len(list) == 0 || env.Report.Count(Blocker) > 0 {
		return nil
	}
	cols := [8][]*string{}
	for _, t := range list {
		id, uid, club := strconv.FormatInt(t.ID, 10), strconv.FormatInt(t.UserID, 10), strconv.FormatInt(env.ClubID, 10)
		name, hash := t.Name, t.Hash
		for i, v := range []*string{&id, &club, &uid, &name, &hash, t.CreatedAt, t.LastUsedAt, t.ExpiresAt} {
			cols[i] = append(cols[i], v)
		}
	}
	tag, err := env.Target.Exec(ctx, `INSERT INTO mobile_api_tokens (id, club_id, user_id, name, token_hash, created_at, last_used_at, expires_at)
		SELECT u.id::bigint, u.club::bigint, u.uid::bigint, u.name, u.hash, COALESCE(u.created::timestamptz, now()), u.used::timestamptz, u.exp::timestamptz
		FROM unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[], $8::text[])
		     AS u(id, club, uid, name, hash, created, used, exp)
		ON CONFLICT (id) DO NOTHING`,
		cols[0], cols[1], cols[2], cols[3], cols[4], cols[5], cols[6], cols[7])
	if err != nil {
		return fmt.Errorf("pg: mobile_api_tokens: %w", err)
	}
	stat.Written = tag.RowsAffected()
	if stat.Written != int64(len(list)) {
		env.Add(Blocker, CodeMobileToken, "mobile_api_tokens", "id", "", "",
			"id токена уже занят в zafirogo (токен другого клуба?) — проверьте вручную")
	}
	return nil
}

func (e *integrations) verifyTokens(ctx context.Context, env *Env, list []mobileToken, stat *TableStat) error {
	rows, err := env.Target.Query(ctx, "SELECT id, user_id, token_hash FROM mobile_api_tokens WHERE club_id = $1", env.ClubID)
	if err != nil {
		return fmt.Errorf("pg: mobile_api_tokens: %w", err)
	}
	type tok struct {
		ID, UserID int64
		Hash       string
	}
	got, err := pgx.CollectRows(rows, pgx.RowToStructByPos[tok])
	if err != nil {
		return err
	}
	byID := make(map[int64]tok, len(got))
	for _, t := range got {
		byID[t.ID] = t
	}
	stat.Target = int64(len(got))
	for _, t := range list {
		p, ok := byID[t.ID]
		if !ok || p.UserID != t.UserID || strings.TrimSpace(p.Hash) != t.Hash {
			env.Add(Blocker, CodeVerifyMismatch, "mobile_api_tokens", "", "", strconv.FormatInt(t.ID, 10),
				"токен мобильного приложения не перенесён или отличается")
		}
	}
	return nil
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// knownUsers — какие из аккаунтов есть в Postgres или переносятся с клубом (перенесены с клубом или раньше).
func (r *run) knownUsers(ctx context.Context, ids []int64) (map[int64]bool, error) {
	if len(ids) == 0 {
		return map[int64]bool{}, nil
	}
	out, err := existingIDs(ctx, r.pg, "users", ids)
	if err != nil {
		return nil, err
	}
	for _, id := range r.userIDs { // plan: аккаунты клуба ещё не записаны
		out[id] = true
	}
	return out, nil
}
