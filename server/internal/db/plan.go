package db

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Планы запросов с необязательными фильтрами (PB-02, docs/open-questions/audit-perf.md).
//
// pgx по умолчанию готовит запрос один раз на соединение (именованный prepared statement), и после пяти выполнений
// Postgres переходит на общий (generic) план, не зависящий от значений параметров. Для запросов вида
// `($search = ” OR name ILIKE $search)`, `($status = ” OR status = $status)`, `($x IS NULL OR col = $x)` общий
// план не может ни свернуть условие, ни убрать ненужное соединение, ни выбрать индекс под заданный фильтр: журнал
// действий без фильтров читал 3,1 млн страниц на запрос вместо 32 тысяч.
//
// Такие запросы (customPlanQueries) выполняются в режиме pgx.QueryExecModeCacheDescribe: описание запроса кэшируется,
// а сам запрос идёт безымянным оператором — Postgres планирует его при каждом выполнении с настоящими значениями
// (custom plan). Обращение к серверу по-прежнему одно; цена — планирование на каждый вызов (доли миллисекунды —
// единицы миллисекунд), это запросы списков и поиска, а не горячий путь. Остальные запросы — как раньше.
//
// Список — по именам запросов sqlc (текст запроса начинается с «-- name: Имя»). Тест TestCustomPlanQueriesCoverOptionalFilters
// сверяет его с db/queries: новый запрос с необязательным фильтром без записи здесь — ошибка теста.
var customPlanQueries = map[string]struct{}{
	// Журнал действий (экран и суперадминка).
	"ListActivityLogPage": {}, "CountActivityLogPage": {}, "ListClubActivityLogs": {},
	"SuperadminActivityLogs": {}, "SuperadminCountActivityLogs": {},
	"SuperadminPlatformLogs": {}, "SuperadminCountPlatformLogs": {},
	// Реселлеры (кабинет суперадмина): список с необязательным поиском.
	"ResellerList": {}, "ResellerCount": {},
	// Клиенты: список, счётчик, поиск на кассе (индексы pg_trgm users(name), users(phone) — 0032).
	"AdminListCustomers": {}, "AdminListCustomersBefore": {}, "AdminCountCustomers": {}, "CashSearchCustomers": {},
	"SuperadminUsers": {}, "SuperadminCountUsers": {}, "SuperadminClubs": {}, "SuperadminCountClubs": {},
	// Товары, заказы, ПК.
	"AdminListProducts": {}, "CashListProducts": {}, "StockListProductsPage": {}, "WarehouseCountProducts": {},
	"ShopOrdersPage": {}, "ShopOrdersCount": {},
	// API интеграций и мобильный API персонала.
	"ApiV1Products": {}, "ApiV1Sales": {}, "ApiV1StockMovements": {}, "ApiV1Computers": {}, "ApiV1Tariffs": {},
	"MobileStaffProducts": {}, "MobileStaffOrders": {}, "MobileStaffCustomers": {}, "IntegrationsListDeliveries": {},
	// Прочие списки с необязательным курсором или фильтром.
	"AdminTariffPage": {}, "ComputerHealthHistory": {},
}

// CustomPlan сообщает, выполняется ли запрос sql (текст sqlc с заголовком «-- name: …») с планом под значения.
func CustomPlan(sql string) bool {
	name, ok := queryName(sql)
	if !ok {
		return false
	}
	_, custom := customPlanQueries[name]
	return custom
}

// queryName — имя запроса sqlc из заголовка «-- name: Имя :вид».
func queryName(sql string) (string, bool) {
	rest, ok := strings.CutPrefix(sql, "-- name: ")
	if !ok {
		return "", false
	}
	name, _, ok := strings.Cut(rest, " ")
	return name, ok
}

// planner — store.DBTX поверх транзакции: запросам из customPlanQueries ставит режим выполнения.
type planner struct{ tx pgx.Tx }

// NewQueries — запросы sqlc поверх транзакции с выбором режима плана (как внутри InTenantTx / InPlatformTx) —
// для кода, который открывает транзакцию сам.
func NewQueries(tx pgx.Tx) *store.Queries { return store.New(planner{tx: tx}) }

func (p planner) args(sql string, args []any) []any {
	if !CustomPlan(sql) {
		return args
	}
	out := make([]any, 0, len(args)+1)
	return append(append(out, pgx.QueryExecModeCacheDescribe), args...)
}

func (p planner) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return p.tx.Exec(ctx, sql, p.args(sql, args)...)
}

func (p planner) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return p.tx.Query(ctx, sql, p.args(sql, args)...)
}

func (p planner) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return p.tx.QueryRow(ctx, sql, p.args(sql, args)...)
}
