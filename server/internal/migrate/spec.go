package migrate

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/secretbox"
)

// table — правило переноса одной таблицы: какие строки клуба читать в MySQL и как писать в Postgres.
// Колонки не перечисляются: переносятся все колонки MySQL, у которых есть пара в Postgres (имена в 0001 — как в
// Laravel, docs/schema-changes.md §1); колонка MySQL без пары — блокер (схема разошлась). Колонки Postgres без пары
// в MySQL (новые: payment_method, income_kind, zone_id, …) получают значение по умолчанию — их заполняет M2.
type table struct {
	Name string
	// Where — условие отбора строк клуба в MySQL и его аргументы (clubArg — id клуба).
	Where func(o *Options) (string, []any)
	// PGWhere — то же условие в Postgres для сверки (см. pgWhere); пусто — club_id = {club}.
	PGWhere string
	// OrderBy — порядок чтения (по возрастанию id: ссылки на себя, cash_transactions.refund_of_id).
	OrderBy string
	// UserRefs — колонки со ссылкой на users.id: аккаунты, на которые ссылается клуб, переносятся вместе с ним.
	UserRefs []string
	// Platform — платформенная таблица (без club_id): строки, уже существующие в Postgres (по id), не трогаем.
	Platform bool
	// Omit — колонки, которые не переносятся (в Postgres — значение по умолчанию/NULL), с причиной.
	Omit map[string]string
	// Partitioned — таблица с месячными партициями по created_at (db.PartitionedTables).
	Partitioned bool
	// NoID — у таблицы нет колонки id (составной ключ).
	NoID bool
	// Transform — правило строки расширения (M2) до преобразования типов: значения MySQL текстом, можно менять
	// (row[idx[колонка]]); false — строка не переносится (TableStat.Skipped).
	Transform func(row []*string, idx map[string]int, rowID string) (bool, error)
	// Const — колонки Postgres без пары в MySQL с постоянным значением (текст, приводится к типу колонки).
	Const map[string]string
}

// tableHandler — расширение, которое переносит таблицы из skippedTables (их нет в списке «не переносится»).
type tableHandler interface {
	Tables() []string
}

func clubScope(*Options) (string, []any) { return "club_id = ?", []any{clubArg} }

// coreTables — таблицы ядра переноса (M1) в порядке внешних ключей. Платформенные строки (users, saas_plans,
// clubs) — только те, что относятся к клубу.
func coreTables() []table {
	return []table{
		{Name: "saas_plans", Platform: true, OrderBy: "id", PGWhere: "id = ANY({ids})",
			Where: func(*Options) (string, []any) {
				return "id IN (SELECT saas_plan_id FROM club_subscriptions WHERE club_id = ?)", []any{clubArg}
			}},
		// users — аккаунты участников клуба и все, на кого ссылаются строки клуба (общий аккаунт может быть в
		// нескольких клубах: не дублируется, сливается по id — см. applyUsers). Условие строится из UserRefs.
		{Name: "users", Platform: true, OrderBy: "id", PGWhere: "id = ANY({ids})"},
		// Q-050: ключ установки клуба (зашифрован APP_KEY) не переносится — выпускается заново.
		{Name: "clubs", Platform: true, OrderBy: "id", PGWhere: "id = {club}",
			Where: func(*Options) (string, []any) { return "id = ?", []any{clubArg} },
			Omit: map[string]string{
				"installation_key":        "Q-050: ключ установки клуба выпускается заново",
				"installation_key_hash":   "Q-050: ключ установки клуба выпускается заново",
				"installation_key_prefix": "Q-050: ключ установки клуба выпускается заново",
			},
			// 0027: перенесённый клуб не открыт до go-live (до него его можно удалить и перенести заново, S5-M3-1).
			Const: map[string]string{"migration_state": StateImported}},
		{Name: "club_subscriptions", Where: clubScope, OrderBy: "id"},
		{Name: "club_memberships", Where: clubScope, OrderBy: "id", UserRefs: []string{"user_id"}},
		// Балансы — не копированием поля: строка с нулями, остаток — проводкой migration_opening_balance (Q-004).
		{Name: "member_balances", Where: clubScope, OrderBy: "id", UserRefs: []string{"user_id"},
			Omit: map[string]string{
				// Роль zafiro_platform вставляет только (club_id, user_id, created_at, updated_at) — 0004_rls.sql;
				// на id строки баланса никто не ссылается (ключ — club_id, user_id).
				"id":            "новый id: INSERT роли платформы — только club_id, user_id, даты (0004)",
				"balance":       "входящий остаток — billing.MigrateOpeningBalance (ledger)",
				"bonus_balance": "входящий остаток — billing.MigrateOpeningBalance (ledger)",
			}},
		{Name: "club_invitations", Where: clubScope, OrderBy: "id", UserRefs: []string{"invited_by"}},
		{Name: "product_categories", Where: clubScope, OrderBy: "id"},
		{Name: "products", Where: clubScope, OrderBy: "id"},
		{Name: "stock_movements", Where: clubScope, OrderBy: "id", UserRefs: []string{"user_id"}},
		{Name: "cash_shifts", Where: clubScope, OrderBy: "id", UserRefs: []string{"opened_by", "closed_by"}},
		{Name: "cash_transactions", Where: clubScope, OrderBy: "id", UserRefs: []string{"user_id"}},
		{Name: "club_services", Where: clubScope, OrderBy: "id"},
		{Name: "club_zones", Where: clubScope, OrderBy: "id"},
		{Name: "club_computers", Where: clubScope, OrderBy: "id", UserRefs: []string{"current_session_user_id", "staff_user_id"}},
		{Name: "tariffs", Where: clubScope, OrderBy: "id"},
		{Name: "club_zone_tariff", Where: clubScope, OrderBy: "club_zone_id, tariff_id", NoID: true},
		{Name: "session_purchases", Where: clubScope, OrderBy: "id", UserRefs: []string{"user_id"}},
		{Name: "time_packages", Where: clubScope, OrderBy: "id", UserRefs: []string{"user_id"}},
		{Name: "computer_reservations", Where: clubScope, OrderBy: "id", UserRefs: []string{"user_id", "created_by"}},
		{Name: "friendships", Where: clubScope, OrderBy: "id", UserRefs: []string{"requester_id", "addressee_id"}},
		{Name: "chat_messages", Where: clubScope, OrderBy: "id", UserRefs: []string{"customer_id", "author_id"}},
		{Name: "shop_orders", Where: clubScope, OrderBy: "id", UserRefs: []string{"user_id", "handled_by"}},
		// Новости платформы (club_id NULL) — не поклубные, переносятся отдельно вместе с платформой.
		{Name: "news", Where: clubScope, OrderBy: "id", UserRefs: []string{"created_by"}},
		{Name: "todo_tasks", Where: clubScope, OrderBy: "id", UserRefs: []string{"created_by", "done_by"}},
		// activity_logs хранится вечно (старый код не чистил) — переносится целиком, партиции под старые даты.
		{Name: "activity_logs", Where: clubScope, OrderBy: "id", UserRefs: []string{"user_id"}, Partitioned: true},
		// Q-083: club_request_metrics / club_load_snapshots — «по желанию»: данные клуба, переносим.
		{Name: "club_request_metrics", Where: clubScope, OrderBy: "id"},
		{Name: "club_load_snapshots", Where: clubScope, OrderBy: "id"},
		// Журнал платформы: события, относящиеся к клубу (club_id = клуб); события без клуба — с платформой.
		// Сверка не считает события самого инструмента (migration.club_live — go-live).
		{Name: "platform_logs", Platform: true, Where: clubScope, OrderBy: "id",
			PGWhere: "club_id = {club} AND event NOT LIKE 'migration.%'", UserRefs: []string{"user_id"}},
		// Журнал запросов API — только в пределах срока хранения (integrations.request_log_retention):
		// более старые строки фоновая задача logs:retention всё равно удалила бы вместе с партицией.
		{Name: "api_request_logs", OrderBy: "id", Partitioned: true,
			PGWhere: "club_id = {club} AND created_at >= {cutoff}",
			Where: func(o *Options) (string, []any) {
				return "club_id = ? AND created_at >= ?", []any{clubArg, o.mysqlTime(o.retentionCutoff())}
			}},
	}
}

// skippedTables — тенантные/платформенные таблицы старой схемы, которые ядро (M1) не переносит, с причиной.
// Расширения (M2) берут часть из них на себя (см. Extension).
var skippedTables = []struct{ Name, Where, Reason string }{ // Where: ? — id клуба
	{"api_keys", "club_id = ?", "M2: ключи API (перевыпуск/перенос хэшей)"},
	{"api_idempotency_keys", "api_key_id IN (SELECT id FROM api_keys WHERE club_id = ?)", "M2: вместе с ключами API"},
	{"webhook_endpoints", "club_id = ?", "M2: секрет зашифрован APP_KEY (Q-050)"},
	{"webhook_deliveries", "club_id = ?", "M2: вместе с вебхуками"},
	{"agent_qr_logins", "club_id = ?", "одноразовые QR-коды входа (живут минуты), агенты переустанавливаются"},
	{"transfer_codes", "friendship_id IN (SELECT id FROM friendships WHERE club_id = ?)", "Q-051: коды перевода не переносятся"},
}

// Options — параметры переноса.
type Options struct {
	// Club — id или slug клуба в MySQL.
	Club string
	// SourceTZ — часовой пояс строк времени в MySQL: config('app.timezone') старого Laravel (UTC).
	SourceTZ *time.Location
	// Now — момент переноса (по умолчанию time.Now): срок хранения журналов, created_at без значения.
	Now time.Time
	// APILogRetention — срок хранения api_request_logs (integrations.request_log_retention, 90 дней).
	APILogRetention time.Duration
	// IDReserve — запас последовательностей id над максимальным id таблицы во всей MySQL (Q-029, S5-M1-3).
	IDReserve int64
	// LegacySettingsFile — содержимое storage/app/settings.json клуба legacy без settings (Q-022); nil — нет файла.
	LegacySettingsFile []byte
	// BatchSize — строк в одной вставке.
	BatchSize int
	// Extensions — расширения (M2): производные данные после ядра (DefaultExtensions).
	Extensions []Extension
	// LaravelKeys — APP_KEY старого Laravel (и APP_PREVIOUS_KEYS): расшифровка секретов (Q-050). nil — секреты
	// не переносятся (блокер, если они есть).
	LaravelKeys *LaravelKeys
	// DataKeys — связка ключей данных нового сервера (security.data_keys): шифрование перенесённых секретов.
	DataKeys *secretbox.Box
	// ThawSubscription — статус подписки клуба до заморозки ("active" или "trialing"): приостановленная на время
	// перевода подписка (status = suspended) переносится в этом статусе. "" — как есть.
	ThawSubscription string
	// LaravelStorage — корень диска 'local' старого Laravel (storage/app/private): логотипы, картинки входа и
	// новостей. "" — файлы не переносятся (предупреждение).
	LaravelStorage string
}

func (o *Options) normalize() {
	if o.SourceTZ == nil {
		o.SourceTZ = time.UTC
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	o.Now = o.Now.UTC().Truncate(time.Second)
	if o.APILogRetention <= 0 {
		o.APILogRetention = 90 * 24 * time.Hour
	}
	if o.IDReserve < 0 {
		o.IDReserve = 0
	}
	if o.BatchSize <= 0 {
		o.BatchSize = 2000
	}
}

func (o *Options) retentionCutoff() time.Time { return o.Now.Add(-o.APILogRetention) }

// mysqlTime — момент в формате строк MySQL (Laravel пишет время строкой в app.timezone).
func (o *Options) mysqlTime(t time.Time) string { return t.In(o.SourceTZ).Format(time.DateTime) }

// usersWhere — условие отбора аккаунтов клуба: объединение всех UserRefs таблиц клуба.
func usersWhere(tables []table, o *Options) (string, []any) {
	var parts []string
	var args []any
	for _, t := range append(slices.Clone(tables), userRefTablesM2()...) {
		if len(t.UserRefs) == 0 || t.Where == nil {
			continue
		}
		where, whereArgs := t.Where(o)
		for _, col := range t.UserRefs {
			parts = append(parts, fmt.Sprintf("SELECT %s FROM %s WHERE (%s) AND %s IS NOT NULL", col, t.Name, where, col))
			args = append(args, whereArgs...)
		}
	}
	return "id IN (" + strings.Join(parts, " UNION ") + ")", args
}

// clubArg — маркер «сюда id клуба» в аргументах условий.
type clubMarker struct{}

var clubArg = clubMarker{}

// bindClub подставляет id клуба вместо маркеров clubArg.
func bindClub(args []any, clubID int64) []any {
	out := make([]any, len(args))
	for i, a := range args {
		if _, ok := a.(clubMarker); ok {
			out[i] = clubID
		} else {
			out[i] = a
		}
	}
	return out
}
