package migrate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/i18n"
	"github.com/aitmaganbetov/zafirogo/server/internal/secretbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Платформенный шаг (один раз перед первым клубом; повторный запуск безопасен): languages, translations (поверх
// import-translations и db/seed), platform_settings (токены Telegram — расшифровка APP_KEY и шифрование ключом
// данных, Q-050), новости платформы (club_id NULL) с картинками, журнал платформы без клуба, все тарифы платформы.
//
// Правило повторного запуска (S5-M2-11): строки, которых нет, добавляются всегда; существующие строки (языки,
// переводы, настройки платформы) перезаписываются значениями MySQL только при ПЕРВОМ платформенном переносе (в
// zafirogo ещё нет записи аудита migration.platform_imported) — тогда в Postgres только то, что положил
// import-translations из файлов, а MySQL хранит правки суперадмина. При повторном — расхождения только в отчёте
// (правки суперадмина zafirogo не затираются). Новости, журнал и тарифы — по id: существующие не трогаются.

// PlatformOptions — параметры платформенного шага.
type PlatformOptions struct {
	SourceTZ  *time.Location
	Now       time.Time
	IDReserve int64
	BatchSize int
	// LaravelKeys — APP_KEY старого Laravel (токены Telegram); DataKeys — ключи данных нового сервера.
	LaravelKeys *LaravelKeys
	DataKeys    *secretbox.Box
	// LaravelStorage — диск 'local' старого Laravel (картинки новостей платформы, news/platform/…).
	LaravelStorage string
	// TranslationsSeed — переводы новых строк zafirogo (db/seed/translations-new.*.json, i18n.LoadSeed): добавляются,
	// если строки нет (как import-translations -seed); nil — не загружать.
	TranslationsSeed map[string]i18n.Shipped
}

const platformAuditAction = "migration.platform_imported"

// platformTables — платформенные таблицы с сохранением id (копирование ядром: строки с существующим id не трогаются,
// тарифы — по id и slug).
func platformTables() []table {
	all := func(*Options) (string, []any) { return "1 = 1", nil }
	noClub := func(*Options) (string, []any) { return "club_id IS NULL", nil }
	return []table{
		{Name: "saas_plans", Platform: true, OrderBy: "id", Where: all},
		{Name: "news", Platform: true, OrderBy: "id", Where: noClub},
		{Name: "platform_logs", Platform: true, OrderBy: "id", Where: noClub},
	}
}

type platform struct {
	r     *run
	o     PlatformOptions
	mode  extMode
	first bool // первый платформенный перенос (нет записи аудита)
}

func newPlatform(ctx context.Context, mode string, my *sql.DB, o PlatformOptions) (*platform, error) {
	opts := &Options{SourceTZ: o.SourceTZ, Now: o.Now, IDReserve: o.IDReserve, BatchSize: o.BatchSize,
		LaravelKeys: o.LaravelKeys, DataKeys: o.DataKeys, LaravelStorage: o.LaravelStorage}
	opts.normalize()
	o.Now, o.SourceTZ = opts.Now, opts.SourceTZ
	src, err := openSource(ctx, my)
	if err != nil {
		return nil, err
	}
	rep := newReport("platform " + mode)
	rep.ClubSlug, rep.ClubName = "platform", "платформа"
	return &platform{r: &run{o: opts, src: src, rep: rep, members: map[int64]bool{}}, o: o}, nil
}

// PlatformPlan — сухой прогон платформенного шага.
func PlatformPlan(ctx context.Context, my *sql.DB, pg *pgxpool.Pool, o PlatformOptions) (*Report, error) {
	p, err := newPlatform(ctx, "plan", my, o)
	if err != nil {
		return nil, err
	}
	defer p.r.src.Close()
	p.r.pg, p.mode = pg, modePlan
	if err := p.detectFirst(ctx); err != nil {
		return p.r.rep, err
	}
	return p.r.rep, p.steps(ctx)
}

// PlatformApply — платформенный шаг одной транзакцией (с проверкой до коммита). Блокеры — ErrBlocked.
func PlatformApply(ctx context.Context, my *sql.DB, pg *pgxpool.Pool, o PlatformOptions) (*Report, error) {
	p, err := newPlatform(ctx, "apply", my, o)
	if err != nil {
		return nil, err
	}
	defer p.r.src.Close()
	// Партиции журналов (аудит) — до транзакции, как в Apply клуба.
	if _, err := db.EnsurePartitions(ctx, pg, p.o.Now, 2); err != nil {
		return p.r.rep, fmt.Errorf("partitions: %w", err)
	}
	err = pgx.BeginTxFunc(ctx, pg, pgx.TxOptions{}, func(tx pgx.Tx) error {
		p.r.pg, p.r.write, p.mode = tx, true, modeApply
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('zafirogo:migrate-from-mysql'))"); err != nil {
			return err
		}
		asPlatform, err := setPlatformRole(ctx, tx)
		if err != nil {
			return err
		}
		if err := p.detectFirst(ctx); err != nil {
			return err
		}
		if err := p.steps(ctx); err != nil {
			return err
		}
		if p.r.rep.Count(Blocker) > 0 {
			return ErrBlocked
		}
		if err := p.audit(ctx, tx); err != nil {
			return err
		}
		if asPlatform {
			if _, err := tx.Exec(ctx, "RESET ROLE"); err != nil {
				return err
			}
		}
		if err := p.r.shiftSequences(ctx, tx); err != nil {
			return err
		}
		p.mode = modeVerify
		if err := p.steps(ctx); err != nil {
			return err
		}
		if p.r.rep.Has(Blocker, CodeVerifyMismatch) {
			return ErrVerifyMismatch
		}
		return nil
	})
	return p.r.rep, err
}

// PlatformVerify — сверка платформенных данных с MySQL (только чтение).
func PlatformVerify(ctx context.Context, my *sql.DB, pg *pgxpool.Pool, o PlatformOptions) (*Report, error) {
	p, err := newPlatform(ctx, "verify", my, o)
	if err != nil {
		return nil, err
	}
	defer p.r.src.Close()
	err = pgx.BeginTxFunc(ctx, pg, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		p.r.pg, p.mode = tx, modeVerify
		if _, err := setPlatformRole(ctx, tx); err != nil {
			return err
		}
		return p.steps(ctx)
	})
	if err != nil {
		return p.r.rep, err
	}
	if p.r.rep.Count(Blocker) > 0 {
		return p.r.rep, ErrVerifyMismatch
	}
	return p.r.rep, nil
}

// diffSeverity — значение отличается от MySQL: при первом переносе это ошибка (блокер), при повторном переносе и
// отдельной сверке — ожидаемо (правки суперадмина в zafirogo не затираются), предупреждение.
func (p *platform) diffSeverity() Severity {
	if p.first && p.r.write {
		return Blocker
	}
	return Warning
}

func (p *platform) detectFirst(ctx context.Context) error {
	var done bool
	if err := p.r.pg.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM audit_log WHERE club_id IS NULL AND action = $1)", platformAuditAction).
		Scan(&done); err != nil {
		return fmt.Errorf("pg: platform audit: %w", err)
	}
	p.first = !done
	if done {
		p.r.rep.add(Info, CodePlatform, "", "", "", "",
			"платформа уже переносилась: добавляются только недостающие строки, расхождения — в отчёте")
	}
	return nil
}

func (p *platform) steps(ctx context.Context) error {
	codes, err := p.languages(ctx)
	if err != nil {
		return err
	}
	if err := p.translations(ctx, codes); err != nil {
		return err
	}
	if err := p.seed(ctx); err != nil {
		return err
	}
	if err := p.settings(ctx); err != nil {
		return err
	}
	if err := p.tables(ctx); err != nil {
		return err
	}
	news, err := newsImages(ctx, p.r.src.conn, "club_id IS NULL")
	if err != nil {
		return err
	}
	return transferImages(ctx, p.r.rep, p.r.pg, p.o.LaravelStorage, news, p.mode)
}

// --- languages ---

type language struct {
	Code, Name           string
	IsEnabled, IsSystem  bool
	SortOrder            int64
	CreatedAt, UpdatedAt *string
}

func (p *platform) languages(ctx context.Context) (map[int64]string, error) {
	rows, err := p.r.src.conn.QueryContext(ctx,
		"SELECT id, code, name, is_enabled, is_system, sort_order, created_at, updated_at FROM languages ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("mysql: languages: %w", err)
	}
	codes := map[int64]string{}
	var list []language
	for rows.Next() {
		var id int64
		var l language
		var created, updated sql.NullString
		if err := rows.Scan(&id, &l.Code, &l.Name, &l.IsEnabled, &l.IsSystem, &l.SortOrder, &created, &updated); err != nil {
			_ = rows.Close()
			return nil, err
		}
		l.CreatedAt, l.UpdatedAt = p.pgTime(created), p.pgTime(updated)
		codes[id] = l.Code
		list = append(list, l)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	pgRows, err := p.r.pg.Query(ctx, "SELECT code, name, is_enabled, is_system, sort_order::bigint FROM languages")
	if err != nil {
		return nil, fmt.Errorf("pg: languages: %w", err)
	}
	existing := map[string]language{}
	for pgRows.Next() {
		var l language
		if err := pgRows.Scan(&l.Code, &l.Name, &l.IsEnabled, &l.IsSystem, &l.SortOrder); err != nil {
			pgRows.Close()
			return nil, err
		}
		existing[l.Code] = l
	}
	pgRows.Close()
	if err := pgRows.Err(); err != nil {
		return nil, err
	}
	stat := p.r.rep.table("languages")
	stat.Source = int64(len(list))
	for _, l := range list {
		e, ok := existing[l.Code]
		same := ok && e.Name == l.Name && e.IsEnabled == l.IsEnabled && e.IsSystem == l.IsSystem && e.SortOrder == l.SortOrder
		switch {
		case p.mode == modeVerify:
			switch {
			case !ok:
				p.r.rep.add(Blocker, CodeVerifyMismatch, "languages", "code", l.Code, "", "язык отсутствует")
			case !same:
				p.r.rep.add(p.diffSeverity(), CodeVerifyMismatch, "languages", "code", l.Code, "", "язык отличается от MySQL")
			default:
				stat.Target++
			}
		case !ok:
			if p.mode == modeApply {
				if _, err := p.r.pg.Exec(ctx, `INSERT INTO languages (code, name, is_enabled, is_system, sort_order, created_at, updated_at)
					VALUES ($1, $2, $3, $4, $5, $6::timestamptz, $7::timestamptz)`,
					l.Code, l.Name, l.IsEnabled, l.IsSystem, l.SortOrder, l.CreatedAt, l.UpdatedAt); err != nil {
					return nil, fmt.Errorf("pg: language %s: %w", l.Code, err)
				}
				stat.Written++
			}
		case !same:
			if !p.first {
				p.r.rep.add(Warning, CodePlatform, "languages", "code", l.Code, "", "язык в zafirogo отличается от MySQL — оставлен как есть")
				continue
			}
			if p.mode == modeApply {
				if _, err := p.r.pg.Exec(ctx, `UPDATE languages SET name = $2, is_enabled = $3, is_system = $4, sort_order = $5,
					updated_at = $6::timestamptz WHERE code = $1`, l.Code, l.Name, l.IsEnabled, l.IsSystem, l.SortOrder, l.UpdatedAt); err != nil {
					return nil, fmt.Errorf("pg: language %s: %w", l.Code, err)
				}
				stat.Written++
			}
		}
	}
	return codes, nil
}

// pgTime — строка времени MySQL → timestamptz (RFC 3339) или nil.
func (p *platform) pgTime(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	out, err := (&column{Kind: kindTime}).convert(&v.String, p.r.o.SourceTZ)
	if err != nil {
		p.r.rep.add(Blocker, CodeBadValue, "", "", v.String, "", err.Error())
		return nil
	}
	return out
}

// --- translations ---

type trKey struct{ Code, Group, Hash string }

func (p *platform) pgTranslations(ctx context.Context) (map[trKey]string, error) {
	rows, err := p.r.pg.Query(ctx, `SELECT l.code, t."group", t.key_hash, t.value FROM translations t JOIN languages l ON l.id = t.language_id`)
	if err != nil {
		return nil, fmt.Errorf("pg: translations: %w", err)
	}
	defer rows.Close()
	out := map[trKey]string{}
	for rows.Next() {
		var k trKey
		var v string
		if err := rows.Scan(&k.Code, &k.Group, &k.Hash, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

func (p *platform) translations(ctx context.Context, codes map[int64]string) error {
	rows, err := p.r.src.conn.QueryContext(ctx, "SELECT language_id, `group`, `key`, key_hash, value FROM translations ORDER BY id")
	if err != nil {
		return fmt.Errorf("mysql: translations: %w", err)
	}
	type tr struct {
		Key         trKey
		Text, Value string
	}
	var list []tr
	for rows.Next() {
		var langID int64
		var t tr
		if err := rows.Scan(&langID, &t.Key.Group, &t.Text, &t.Key.Hash, &t.Value); err != nil {
			_ = rows.Close()
			return err
		}
		code, ok := codes[langID]
		if !ok {
			continue // FK в MySQL не даст; на всякий случай
		}
		t.Key.Code = code
		if h := i18n.HashKey(t.Key.Group, t.Text); h != t.Key.Hash {
			p.r.rep.add(Warning, CodePlatform, "translations", "key_hash", "", "", "key_hash не равен sha1(group|key) — перенесён как есть")
		}
		list = append(list, t)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	existing, err := p.pgTranslations(ctx)
	if err != nil {
		return err
	}
	stat := p.r.rep.table("translations")
	stat.Source = int64(len(list))
	var insCode, insGroup, insKey, insHash, insValue []string
	var updCode, updGroup, updHash, updValue []string
	for _, t := range list {
		v, ok := existing[t.Key]
		switch {
		case p.mode == modeVerify:
			switch {
			case !ok:
				p.r.rep.add(Blocker, CodeVerifyMismatch, "translations", t.Key.Code, "", "", "перевод отсутствует")
			case v != t.Value:
				p.r.rep.add(p.diffSeverity(), CodeVerifyMismatch, "translations", t.Key.Code, "", "", "перевод отличается от MySQL")
			default:
				stat.Target++
			}
		case !ok:
			insCode, insGroup, insKey = append(insCode, t.Key.Code), append(insGroup, t.Key.Group), append(insKey, t.Text)
			insHash, insValue = append(insHash, t.Key.Hash), append(insValue, t.Value)
		case v != t.Value:
			if !p.first {
				p.r.rep.add(Warning, CodePlatform, "translations", t.Key.Code, "", "",
					"перевод в zafirogo отличается от MySQL (правка суперадмина?) — оставлен как есть")
				continue
			}
			updCode, updGroup = append(updCode, t.Key.Code), append(updGroup, t.Key.Group)
			updHash, updValue = append(updHash, t.Key.Hash), append(updValue, t.Value)
		}
	}
	if p.mode != modeApply {
		if p.mode == modePlan {
			p.r.rep.Totals["translations: добавить / обновить"] = fmt.Sprintf("%d / %d", len(insCode), len(updCode))
		}
		return nil
	}
	if len(insCode) > 0 {
		tag, err := p.r.pg.Exec(ctx, `INSERT INTO translations (language_id, "group", key, key_hash, value, created_at, updated_at)
			SELECT l.id, u.g, u.k, u.h, u.v, now(), now()
			FROM unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::text[]) AS u(c, g, k, h, v)
			JOIN languages l ON l.code = u.c
			ON CONFLICT (language_id, "group", key_hash) DO NOTHING`, insCode, insGroup, insKey, insHash, insValue)
		if err != nil {
			return fmt.Errorf("pg: translations: %w", err)
		}
		stat.Written += tag.RowsAffected()
	}
	if len(updCode) > 0 {
		tag, err := p.r.pg.Exec(ctx, `UPDATE translations AS t SET value = u.v, updated_at = now()
			FROM unnest($1::text[], $2::text[], $3::text[], $4::text[]) AS u(c, g, h, v), languages l
			WHERE l.code = u.c AND t.language_id = l.id AND t."group" = u.g AND t.key_hash = u.h`,
			updCode, updGroup, updHash, updValue)
		if err != nil {
			return fmt.Errorf("pg: translations: %w", err)
		}
		stat.Written += tag.RowsAffected()
	}
	return nil
}

// seed — db/seed/translations-new.*.json: строки, которых нет (как import-translations -seed).
func (p *platform) seed(ctx context.Context) error {
	if p.o.TranslationsSeed == nil || p.mode != modeApply {
		return nil
	}
	q := store.New(p.r.pg)
	var inserted int64
	for _, code := range sortedKeys(p.o.TranslationsSeed) {
		langID, err := q.I18nLanguageID(ctx, code)
		if errors.Is(err, pgx.ErrNoRows) {
			p.r.rep.add(Warning, CodePlatform, "translations", code, "", "", "язык из db/seed отсутствует — его строки не добавлены")
			continue
		}
		if err != nil {
			return err
		}
		s := p.o.TranslationsSeed[code]
		for _, g := range s.Groups() {
			for _, k := range s.Keys(g) {
				n, err := q.I18nInsertMissing(ctx, store.I18nInsertMissingParams{
					LanguageID: langID, Grp: g, Key: k, KeyHash: i18n.HashKey(g, k), Value: s[g][k],
				})
				if err != nil {
					return err
				}
				inserted += n
			}
		}
	}
	p.r.rep.Totals["translations: добавлено из db/seed"] = strconv.FormatInt(inserted, 10)
	return nil
}

// --- platform_settings ---

type platformSetting struct {
	Key                  string
	Value                *string // JSON (токен — уже зашифрован ключом данных)
	Plain                map[string]any
	UpdatedBy            *int64
	CreatedAt, UpdatedAt *string
}

// settings — platform_settings: токены Telegram ({"token": Crypt::encryptString(…)}) → secretbox без контекста
// (как telegram.Store.Read: Box.Open). Остальное — как есть. updated_by без аккаунта в zafirogo — NULL.
func (p *platform) settings(ctx context.Context) error {
	rows, err := p.r.src.conn.QueryContext(ctx, "SELECT `key`, value, updated_by, created_at, updated_at FROM platform_settings ORDER BY `key`")
	if err != nil {
		return fmt.Errorf("mysql: platform_settings: %w", err)
	}
	var list []platformSetting
	for rows.Next() {
		var s platformSetting
		var value, created, updated sql.NullString
		var by sql.NullInt64
		if err := rows.Scan(&s.Key, &value, &by, &created, &updated); err != nil {
			_ = rows.Close()
			return err
		}
		if value.Valid {
			s.Value = &value.String
		}
		if by.Valid {
			s.UpdatedBy = &by.Int64
		}
		s.CreatedAt, s.UpdatedAt = p.pgTime(created), p.pgTime(updated)
		list = append(list, s)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	stat := p.r.rep.table("platform_settings")
	stat.Source = int64(len(list))
	for i := range list {
		s := &list[i]
		plain, sealed, ok := p.sealSetting(s.Key, s.Value, p.mode != modeVerify)
		if !ok {
			continue
		}
		s.Plain = plain
		var cur *string
		err := p.r.pg.QueryRow(ctx, "SELECT value::text FROM platform_settings WHERE key = $1", s.Key).Scan(&cur)
		exists := err == nil
		if err != nil && !isNoRows(err) {
			return fmt.Errorf("pg: platform setting: %w", err)
		}
		same := exists && reflect.DeepEqual(p.openSetting(cur), plain)
		switch {
		case p.mode == modeVerify:
			switch {
			case !exists:
				p.r.rep.add(Blocker, CodeVerifyMismatch, "platform_settings", "key", s.Key, "", "настройки платформы нет")
			case !same:
				p.r.rep.add(p.diffSeverity(), CodeVerifyMismatch, "platform_settings", "key", s.Key, "", "настройка платформы отличается от MySQL")
			default:
				stat.Target++
			}
			continue
		case same:
			continue
		case exists && !p.first:
			p.r.rep.add(Warning, CodePlatform, "platform_settings", "key", s.Key, "", "настройка в zafirogo отличается от MySQL — оставлена как есть")
			continue
		}
		if p.mode != modeApply {
			continue
		}
		by := s.UpdatedBy
		if by != nil {
			known, err := existingIDs(ctx, p.r.pg, "users", []int64{*by})
			if err != nil {
				return err
			}
			if !known[*by] {
				by = nil // аккаунт ещё не перенесён (FK ON DELETE SET NULL)
			}
		}
		if _, err := p.r.pg.Exec(ctx, `INSERT INTO platform_settings (key, value, updated_by, created_at, updated_at)
			VALUES ($1, $2::jsonb, $3, $4::timestamptz, $5::timestamptz)
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = EXCLUDED.updated_at`,
			s.Key, sealed, by, s.CreatedAt, s.UpdatedAt); err != nil {
			return fmt.Errorf("pg: platform setting %s: %w", s.Key, err)
		}
		stat.Written++
	}
	return nil
}

// sealSetting — значение настройки: открытое (для сравнения, токен расшифрован) и для записи (токен — secretbox).
// ok = false — блокер (токен не расшифровать/не зашифровать).
func (p *platform) sealSetting(key string, value *string, seal bool) (map[string]any, *string, bool) {
	if value == nil {
		return nil, nil, true
	}
	var obj map[string]any
	if json.Unmarshal([]byte(*value), &obj) != nil || obj == nil {
		return map[string]any{"": *value}, value, true
	}
	token, isStr := obj["token"].(string)
	if !isStr || token == "" {
		return obj, value, true
	}
	plain, ok := openLaravel(p.o.LaravelKeys, p.r.rep, "platform_settings", key, "", token)
	if !ok {
		return nil, nil, false
	}
	open := cloneMap(obj)
	open["token"] = plain
	if !seal {
		return open, nil, true
	}
	if p.o.DataKeys == nil {
		p.r.rep.add(Blocker, CodeSecret, "platform_settings", key, "", "",
			"нет ключей данных нового сервера (ZAFIRO_SECURITY_DATA_KEYS): токен не зашифровать")
		return nil, nil, false
	}
	sealed, err := p.o.DataKeys.Seal(plain)
	if err != nil {
		p.r.rep.add(Blocker, CodeSecret, "platform_settings", key, "", "", "токен не зашифровать ключом данных")
		return nil, nil, false
	}
	obj["token"] = sealed
	b, err := json.Marshal(obj)
	if err != nil {
		return nil, nil, false
	}
	out := string(b)
	return open, &out, true
}

// openSetting — значение из Postgres для сравнения: токен расшифрован ключом данных (не вышло — как есть).
func (p *platform) openSetting(v *string) map[string]any {
	if v == nil {
		return nil
	}
	var obj map[string]any
	if json.Unmarshal([]byte(*v), &obj) != nil || obj == nil {
		var s any
		_ = json.Unmarshal([]byte(*v), &s)
		if str, ok := s.(string); ok {
			return map[string]any{"": str}
		}
		return map[string]any{"": *v}
	}
	if token, ok := obj["token"].(string); ok && p.o.DataKeys != nil && secretbox.IsSealed(token) {
		if plain, err := p.o.DataKeys.Open(token); err == nil {
			obj["token"] = plain
		}
	}
	return obj
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// --- saas_plans, news, platform_logs ---

func (p *platform) tables(ctx context.Context) error {
	known, err := p.knownPlatformUsers(ctx)
	if err != nil {
		return err
	}
	for _, t := range platformTables() {
		var refs []string
		switch t.Name {
		case "news":
			refs = []string{"created_by"}
		case "platform_logs":
			refs = []string{"user_id"}
		}
		if len(refs) > 0 {
			t.Transform = func(row []*string, idx map[string]int, rowID string) (bool, error) {
				for _, c := range refs {
					v := row[idx[c]]
					if v == nil {
						continue
					}
					id, _ := strconv.ParseInt(*v, 10, 64)
					if !known[id] {
						row[idx[c]] = nil // аккаунт не в zafirogo (суперадмин без клуба — переносится с первым клубом)
						if p.mode != modeVerify {
							p.r.rep.add(Info, CodePlatform, t.Name, c, "", rowID, "автор/пользователь ещё не в zafirogo — NULL")
						}
					}
				}
				return true, nil
			}
		}
		if p.mode == modeVerify {
			if err := p.verifyTable(ctx, t); err != nil {
				return err
			}
			continue
		}
		if err := p.r.copyTable(ctx, t); err != nil {
			return err
		}
	}
	return nil
}

func (p *platform) knownPlatformUsers(ctx context.Context) (map[int64]bool, error) {
	ids, err := p.r.mysqlIDs(ctx, "users", `id IN (SELECT created_by FROM news WHERE club_id IS NULL AND created_by IS NOT NULL
		UNION SELECT user_id FROM platform_logs WHERE club_id IS NULL AND user_id IS NOT NULL)`, nil)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return map[int64]bool{}, nil
	}
	return existingIDs(ctx, p.r.pg, "users", ids)
}

// verifyTable — все строки MySQL есть в Postgres (по id; тарифы — тот же slug).
func (p *platform) verifyTable(ctx context.Context, t table) error {
	where, args := t.Where(p.r.o)
	ids, err := p.r.mysqlIDs(ctx, t.Name, where, args)
	if err != nil {
		return err
	}
	var n int64
	if err := p.r.pg.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s WHERE id = ANY($1)", pgx.Identifier{t.Name}.Sanitize()), ids).
		Scan(&n); err != nil {
		return fmt.Errorf("pg: count %s: %w", t.Name, err)
	}
	stat := p.r.rep.table(t.Name)
	stat.Source, stat.Target = int64(len(ids)), n
	if n != int64(len(ids)) {
		p.r.rep.add(Blocker, CodeVerifyMismatch, t.Name, "", "", "", fmt.Sprintf("строк: MySQL %d, Postgres %d", len(ids), n))
	}
	return nil
}

// audit — запись аудита платформенного переноса (маркер «первый перенос выполнен»).
func (p *platform) audit(ctx context.Context, tx pgx.Tx) error {
	after, err := json.Marshal(map[string]any{"source": "mysql", "first": p.first, "tables": p.written()})
	if err != nil {
		return err
	}
	subject := "platform"
	return store.New(tx).InsertAuditLog(ctx, store.InsertAuditLogParams{
		ActorKind: "system", Category: "settings", Action: platformAuditAction, SubjectType: &subject, After: after,
	})
}

func (p *platform) written() map[string]int64 {
	out := map[string]int64{}
	for _, t := range p.r.rep.Tables {
		out[t.Table] = t.Written
	}
	return out
}
