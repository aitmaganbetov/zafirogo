// Package ledger — журнал денежных операций с двойной записью (db/migrations/0002_ledger.sql).
//
// Все движения денег — только через Post (или Begin + Pending.Post): операция с ключом идемпотентности
// и проводками, сумма которых равна 0. Баланс клиента (member_balances.balance/bonus_balance) — проекция:
// его сдвигает триггер БД при вставке проводки по счёту клиента в той же транзакции; прямое изменение
// запрещено правами роли и триггером-сторожем.
//
// Знак суммы проводки: > 0 — кредит, < 0 — дебет. Для счетов клиента (обязательство клуба перед клиентом)
// это «+ баланс растёт, − уменьшается»; для кассы (актив) наоборот: приход в кассу — дебет (−).
// Схема счетов и типовые проводки — docs/schema-changes.md, раздел «Как реализовано (Этап 1)».
//
// Вызывать только внутри db.InTenantTx того же клуба.
package ledger

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// Kind — вид операции (ledger_transactions.kind, CHECK в 0002_ledger.sql и 0014_stage1_decisions.sql).
type Kind string

// Виды операций.
const (
	KindTopUp                   Kind = "top_up"                    // пополнение на кассе, BR-053 (+ бонус, BR-041)
	KindTopUpRefund             Kind = "top_up_refund"             // возврат пополнения, BR-055
	KindOwnerTopUp              Kind = "owner_top_up"              // пополнение владельцем без кассы, BR-058
	KindOwnerWriteOff           Kind = "owner_write_off"           // списание владельцем без кассы, BR-058
	KindInitialBalance          Kind = "initial_balance"           // начальный баланс нового клиента, BR-059
	KindFriendTransfer          Kind = "friend_transfer"           // перевод другу, BR-091/092
	KindTariffPurchase          Kind = "tariff_purchase"           // BR-020
	KindMinutesPurchase         Kind = "minutes_purchase"          // BR-021
	KindPackageStart            Kind = "package_start"             // BR-024 (только минуты)
	KindSessionRefund           Kind = "session_refund"            // BR-032
	KindPackageKept             Kind = "package_kept"              // BR-032 (только минуты)
	KindPackageExpired          Kind = "package_expired"           // BR-034, допущение Q-011 (только минуты)
	KindShopOrder               Kind = "shop_order"                // BR-075
	KindShopOrderCancel         Kind = "shop_order_cancel"         // BR-077
	KindPOSSale                 Kind = "pos_sale"                  // BR-052
	KindPOSRefund               Kind = "pos_refund"                // BR-055
	KindCashIncome              Kind = "cash_income"               // BR-054
	KindCashExpense             Kind = "cash_expense"              // BR-054
	KindGuestSession            Kind = "guest_session"             // BR-038
	KindMigrationOpeningBalance Kind = "migration_opening_balance" // перенос из MySQL
	// KindCashShiftOpening — выравнивание кассы при открытии смены: разница начального остатка смены и остатка
	// cash_drawer по журналу (деньги, внесённые или изъятые между сменами), счёт opening_balance.
	// Решение CSQ-1 (docs/DECISIONS.md), миграция 0014.
	KindCashShiftOpening Kind = "cash_shift_opening"
)

var validKinds = map[Kind]bool{
	KindTopUp: true, KindTopUpRefund: true, KindOwnerTopUp: true, KindOwnerWriteOff: true,
	KindInitialBalance: true, KindFriendTransfer: true, KindTariffPurchase: true, KindMinutesPurchase: true,
	KindPackageStart: true, KindSessionRefund: true, KindPackageKept: true, KindPackageExpired: true,
	KindShopOrder: true, KindShopOrderCancel: true, KindPOSSale: true, KindPOSRefund: true,
	KindCashIncome: true, KindCashExpense: true, KindGuestSession: true, KindMigrationOpeningBalance: true,
	KindCashShiftOpening: true,
}

// Valid сообщает, что вид операции известен схеме.
func (k Kind) Valid() bool { return validKinds[k] }

// Kinds — все виды операций, известные схеме (по алфавиту).
func Kinds() []Kind {
	out := make([]Kind, 0, len(validKinds))
	for k := range validKinds {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// AccountKind — вид счёта (ledger_accounts.kind).
type AccountKind string

// Виды счетов.
const (
	AccountMemberMain      AccountKind = "member_main"      // основной баланс клиента
	AccountMemberBonus     AccountKind = "member_bonus"     // бонусный баланс клиента
	AccountCashDrawer      AccountKind = "cash_drawer"      // касса клуба (актив; один остаток на все способы оплаты)
	AccountRevenueTime     AccountKind = "revenue_time"     // выручка за время
	AccountRevenueShop     AccountKind = "revenue_shop"     // выручка магазина агента
	AccountRevenuePOS      AccountKind = "revenue_pos"      // выручка POS (товары и услуги)
	AccountBonusFund       AccountKind = "bonus_fund"       // бонусный фонд: выпуск (дебет) и расход/возврат (кредит)
	AccountCashOther       AccountKind = "cash_other"       // прочие приходы/расходы кассы
	AccountOwnerAdjustment AccountKind = "owner_adjustment" // корректировки владельцем и начальные балансы
	AccountOpeningBalance  AccountKind = "opening_balance"  // входящие остатки переноса
)

var validAccountKinds = map[AccountKind]bool{
	AccountMemberMain: true, AccountMemberBonus: true, AccountCashDrawer: true, AccountRevenueTime: true,
	AccountRevenueShop: true, AccountRevenuePOS: true, AccountBonusFund: true, AccountCashOther: true,
	AccountOwnerAdjustment: true, AccountOpeningBalance: true,
}

// Member сообщает, что это счёт клиента (с проекцией в member_balances).
func (k AccountKind) Member() bool { return k == AccountMemberMain || k == AccountMemberBonus }

// Account — счёт: вид + клиент (только для счетов клиента). Клуб — из операции.
type Account struct {
	Kind   AccountKind
	UserID int64
}

// Main — основной баланс клиента.
func Main(userID int64) Account { return Account{Kind: AccountMemberMain, UserID: userID} }

// Bonus — бонусный баланс клиента.
func Bonus(userID int64) Account { return Account{Kind: AccountMemberBonus, UserID: userID} }

// ClubAccount — счёт клуба (касса, выручка, фонды).
func ClubAccount(kind AccountKind) Account { return Account{Kind: kind} }

func (a Account) String() string {
	if a.Kind.Member() {
		return fmt.Sprintf("%s:%d", a.Kind, a.UserID)
	}
	return string(a.Kind)
}

func (a Account) valid() bool {
	if !validAccountKinds[a.Kind] {
		return false
	}
	return a.Kind.Member() == (a.UserID > 0)
}

// Entry — проводка: счёт и сумма со знаком (тиыны).
type Entry struct {
	Account Account
	Amount  money.Tiyn
}

// MinuteEntry — движение минут пакета (ledger минут; минуты — не деньги, сумма не обязана быть 0).
type MinuteEntry struct {
	UserID        int64
	TimePackageID int64 // 0 — нет
	TariffID      int64 // 0 — нет
	Minutes       int64 // + сохранено/начислено, − запущено/сгорело
}

// Refs — ссылки операции на документы (без FK: документы чистятся).
type Refs struct {
	CashShiftID           int64
	CashTransactionID     int64
	SessionID             string
	ShopOrderID           int64
	ReversesTransactionID int64
}

// Operation — денежная операция.
type Operation struct {
	ClubID         int64
	Kind           Kind
	IdempotencyKey string
	Entries        []Entry
	Minutes        []MinuteEntry
	Meta           json.RawMessage
	ActorUserID    int64 // 0 — система
	Refs           Refs
}

// PostedEntry — записанная проводка.
type PostedEntry struct {
	Account Account
	Amount  money.Tiyn
	// BalanceAfter — баланс счёта клиента после проводки (только для счетов клиента).
	BalanceAfter money.Tiyn
}

// Result — записанная (или ранее записанная, Replayed) операция.
type Result struct {
	TransactionID int64
	CreatedAt     time.Time
	Kind          Kind
	Replayed      bool
	Entries       []PostedEntry
	Minutes       []MinuteEntry
	Meta          json.RawMessage
	Refs          Refs
}

// BalanceAfter возвращает баланс счёта клиента после операции, если операция его меняла.
func (r Result) BalanceAfter(a Account) (money.Tiyn, bool) {
	for _, e := range r.Entries {
		if e.Account == a && a.Kind.Member() {
			return e.BalanceAfter, true
		}
	}
	return 0, false
}

// Amount возвращает сумму проводки по счёту (0, если счёт не участвовал).
func (r Result) Amount(a Account) money.Tiyn {
	var sum money.Tiyn
	for _, e := range r.Entries {
		if e.Account == a {
			sum += e.Amount
		}
	}
	return sum
}

// MaxMemberBalance — предел баланса клиента: decimal(12,2) старой схемы (9 999 999 999.99 тенге).
// В MySQL запись больше — ошибка strict-режима и откат транзакции (BR-001, BR-053).
const MaxMemberBalance money.Tiyn = 999_999_999_999

// Ошибки.
var (
	// ErrIdempotencyConflict — ключ уже использован операцией с другим телом запроса.
	ErrIdempotencyConflict = errors.New("ledger: idempotency key reused with a different request")
	// ErrUnbalanced — сумма проводок операции не равна 0.
	ErrUnbalanced = errors.New("ledger: entries do not sum to zero")
	// ErrInvalidOperation — операция составлена неверно (вид, ключ, счета, нулевые суммы).
	ErrInvalidOperation = errors.New("ledger: invalid operation")
	// ErrTenantMismatch — транзакция не в контексте клуба операции (нет db.InTenantTx или другой клуб).
	ErrTenantMismatch = errors.New("ledger: transaction is not scoped to the operation's club")
	// ErrBalanceNotFound — у клиента нет строки member_balances в этом клубе.
	ErrBalanceNotFound = errors.New("ledger: member balance not found")
	// ErrInsufficientFunds — списание увело бы баланс клиента в минус.
	ErrInsufficientFunds = errors.New("ledger: insufficient funds")
	// ErrBalanceOverflow — баланс клиента превысил бы MaxMemberBalance.
	ErrBalanceOverflow = errors.New("ledger: member balance overflow")
	// ErrAlreadyPosted — Pending.Post вызван второй раз.
	ErrAlreadyPosted = errors.New("ledger: pending operation already posted")
)

// BalanceNotFoundError — нет member_balances клиента (errors.Is(err, ErrBalanceNotFound)).
type BalanceNotFoundError struct{ UserID int64 }

func (e *BalanceNotFoundError) Error() string {
	return fmt.Sprintf("ledger: member balance of user %d not found", e.UserID)
}

// Unwrap связывает с ErrBalanceNotFound.
func (e *BalanceNotFoundError) Unwrap() error { return ErrBalanceNotFound }

// InsufficientFundsError — на счёте клиента меньше суммы списания (errors.Is(err, ErrInsufficientFunds)).
type InsufficientFundsError struct {
	Account Account
	Balance money.Tiyn
	Debit   money.Tiyn // модуль списания
}

func (e *InsufficientFundsError) Error() string {
	return fmt.Sprintf("ledger: insufficient funds on %s: balance %s, debit %s", e.Account, e.Balance, e.Debit)
}

// Unwrap связывает с ErrInsufficientFunds.
func (e *InsufficientFundsError) Unwrap() error { return ErrInsufficientFunds }
