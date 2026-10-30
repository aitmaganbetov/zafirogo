package ledger

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// MaxIdempotencyKeyLen — длина ledger_idempotency.idempotency_key (varchar(200)).
const MaxIdempotencyKeyLen = 200

// Pending — захваченный ключ идемпотентности: операция ещё не записана, ключ уже наш до конца транзакции
// (параллельный запрос с тем же ключом ждёт её COMMIT/ROLLBACK на уникальном индексе).
type Pending struct {
	clubID    int64
	kind      Kind
	key       string
	txID      int64
	createdAt time.Time
	posted    bool
}

// TransactionID — id будущей операции.
func (p *Pending) TransactionID() int64 { return p.txID }

// Begin захватывает ключ идемпотентности операции вида kind. request — тело запроса (то, что пришло от
// клиента/кассы); его отпечаток (sha256 канонического JSON) хранится с ключом.
//
// Если операция с этим ключом уже записана: тот же отпечаток — возвращает её результат (Replayed = true,
// Pending = nil); другой — ErrIdempotencyConflict.
//
// Вызывать до блокировок и проверок, чтобы повтор запроса получил прежний ответ, а не ошибку проверки
// (например, «недостаточно средств» после первого успешного списания).
func Begin(ctx context.Context, q *store.Queries, clubID int64, kind Kind, key string, request any) (*Pending, *Result, error) {
	if err := checkHeader(clubID, kind, key); err != nil {
		return nil, nil, err
	}
	if err := checkTenant(ctx, q, clubID); err != nil {
		return nil, nil, err
	}
	fp, err := fingerprint(kind, request)
	if err != nil {
		return nil, nil, err
	}
	row, err := q.ClaimLedgerIdempotencyKey(ctx, store.ClaimLedgerIdempotencyKeyParams{
		ClubID: clubID, IdempotencyKey: key, Fingerprint: fp,
	})
	if err == nil {
		return &Pending{clubID: clubID, kind: kind, key: key, txID: row.TransactionID, createdAt: row.TransactionCreatedAt}, nil, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, fmt.Errorf("ledger: claim idempotency key: %w", err)
	}
	prev, err := q.GetLedgerIdempotencyKey(ctx, store.GetLedgerIdempotencyKeyParams{ClubID: clubID, IdempotencyKey: key})
	if err != nil {
		return nil, nil, fmt.Errorf("ledger: read idempotency key: %w", err)
	}
	if !bytes.Equal(prev.Fingerprint, fp) {
		return nil, nil, ErrIdempotencyConflict
	}
	res, err := load(ctx, q, clubID, prev.TransactionID, prev.TransactionCreatedAt)
	if err != nil {
		return nil, nil, err
	}
	if res.Kind != kind {
		return nil, nil, ErrIdempotencyConflict
	}
	res.Replayed = true
	return nil, &res, nil
}

// Post записывает операцию с готовыми проводками: захват ключа (отпечаток — вид, проводки, минуты, meta,
// ссылки), блокировка балансов затронутых клиентов, проверки, запись. Повтор с тем же ключом и тем же
// телом возвращает прежний результат (Replayed = true), с другим телом — ErrIdempotencyConflict.
func Post(ctx context.Context, q *store.Queries, op Operation) (Result, error) {
	p, replay, err := Begin(ctx, q, op.ClubID, op.Kind, op.IdempotencyKey, staticRequest(op))
	if err != nil {
		return Result{}, err
	}
	if replay != nil {
		return *replay, nil
	}
	return p.Post(ctx, q, op)
}

// Post записывает операцию под захваченным ключом. ClubID, Kind и IdempotencyKey операции, если заданы,
// должны совпадать с Begin. Балансы клиентов из проводок блокируются (FOR UPDATE, по возрастанию user_id;
// если вызывающий уже заблокировал их LockBalances, повторная блокировка ничего не ждёт).
func (p *Pending) Post(ctx context.Context, q *store.Queries, op Operation) (Result, error) {
	if p == nil || p.posted {
		return Result{}, ErrAlreadyPosted
	}
	if (op.ClubID != 0 && op.ClubID != p.clubID) || (op.Kind != "" && op.Kind != p.kind) ||
		(op.IdempotencyKey != "" && op.IdempotencyKey != p.key) {
		return Result{}, fmt.Errorf("%w: operation does not match the claimed key", ErrInvalidOperation)
	}
	op.ClubID, op.Kind, op.IdempotencyKey = p.clubID, p.kind, p.key
	if err := validate(op); err != nil {
		return Result{}, err
	}

	members := memberUsers(op.Entries)
	balances, err := LockBalances(ctx, q, op.ClubID, members...)
	if err != nil {
		return Result{}, err
	}
	if err := checkBalances(op, balances); err != nil {
		return Result{}, err
	}

	accountIDs, err := upsertAccounts(ctx, q, op.ClubID, op.Entries)
	if err != nil {
		return Result{}, err
	}

	meta := op.Meta
	if len(meta) == 0 {
		meta = nil
	}
	if err := q.InsertLedgerTransaction(ctx, store.InsertLedgerTransactionParams{
		ID:                    p.txID,
		ClubID:                op.ClubID,
		Kind:                  string(op.Kind),
		IdempotencyKey:        op.IdempotencyKey,
		ActorUserID:           nonZero(op.ActorUserID),
		CashShiftID:           nonZero(op.Refs.CashShiftID),
		CashTransactionID:     nonZero(op.Refs.CashTransactionID),
		SessionID:             nonEmpty(op.Refs.SessionID),
		ShopOrderID:           nonZero(op.Refs.ShopOrderID),
		ReversesTransactionID: nonZero(op.Refs.ReversesTransactionID),
		Meta:                  meta,
		CreatedAt:             p.createdAt,
	}); err != nil {
		return Result{}, fmt.Errorf("ledger: insert transaction: %w", err)
	}

	res := Result{TransactionID: p.txID, CreatedAt: p.createdAt, Kind: op.Kind, Meta: meta, Refs: op.Refs}
	if len(op.Entries) > 0 {
		ids := make([]int64, len(op.Entries))
		amounts := make([]int64, len(op.Entries))
		for i, e := range op.Entries {
			ids[i] = accountIDs[e.Account]
			amounts[i] = int64(e.Amount)
		}
		rows, err := q.InsertLedgerEntries(ctx, store.InsertLedgerEntriesParams{
			ClubID: op.ClubID, TransactionID: p.txID, CreatedAt: p.createdAt, AccountIds: ids, Amounts: amounts,
		})
		if err != nil {
			return Result{}, mapDBError(err)
		}
		byAccountID := make(map[int64]store.InsertLedgerEntriesRow, len(rows))
		for _, r := range rows {
			byAccountID[r.AccountID] = r
		}
		for _, e := range op.Entries {
			pe := PostedEntry{Account: e.Account, Amount: e.Amount}
			if r, ok := byAccountID[accountIDs[e.Account]]; ok && r.BalanceAfter != nil {
				pe.BalanceAfter = money.Tiyn(*r.BalanceAfter)
			}
			res.Entries = append(res.Entries, pe)
		}
	}
	if len(op.Minutes) > 0 {
		n := len(op.Minutes)
		users, packages, tariffs, minutes := make([]int64, n), make([]int64, n), make([]int64, n), make([]int64, n)
		for i, m := range op.Minutes {
			users[i], packages[i], tariffs[i], minutes[i] = m.UserID, m.TimePackageID, m.TariffID, m.Minutes
		}
		if err := q.InsertLedgerMinuteEntries(ctx, store.InsertLedgerMinuteEntriesParams{
			ClubID: op.ClubID, TransactionID: p.txID, CreatedAt: p.createdAt,
			UserIds: users, TimePackageIds: packages, TariffIds: tariffs, Minutes: minutes,
		}); err != nil {
			return Result{}, mapDBError(err)
		}
		res.Minutes = slices.Clone(op.Minutes)
	}
	p.posted = true
	return res, nil
}

// MemberBalance — заблокированный баланс клиента.
type MemberBalance struct {
	UserID int64
	Main   money.Tiyn
	Bonus  money.Tiyn
}

// Balances — заблокированные балансы по user_id.
type Balances map[int64]MemberBalance

// Of возвращает баланс счёта клиента.
func (b Balances) Of(a Account) money.Tiyn {
	mb := b[a.UserID]
	if a.Kind == AccountMemberBonus {
		return mb.Bonus
	}
	return mb.Main
}

// LockBalances блокирует (SELECT … FOR UPDATE) строки member_balances клиентов в порядке user_id —
// как lockForUpdate() старого кода (BR-005; два клиента — Friends::transfer(), app/Services/Friends.php ~стр. 128).
// Нет строки у кого-то из клиентов — *BalanceNotFoundError.
func LockBalances(ctx context.Context, q *store.Queries, clubID int64, userIDs ...int64) (Balances, error) {
	ids := slices.Clone(userIDs)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	out := make(Balances, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.LockMemberBalances(ctx, store.LockMemberBalancesParams{ClubID: clubID, UserIds: ids})
	if err != nil {
		return nil, fmt.Errorf("ledger: lock balances: %w", err)
	}
	for _, r := range rows {
		out[r.UserID] = MemberBalance{UserID: r.UserID, Main: money.Tiyn(r.Balance), Bonus: money.Tiyn(r.BonusBalance)}
	}
	for _, id := range ids {
		if _, ok := out[id]; !ok {
			return nil, &BalanceNotFoundError{UserID: id}
		}
	}
	return out, nil
}

// Load читает записанную операцию клуба.
func Load(ctx context.Context, q *store.Queries, clubID, transactionID int64, createdAt time.Time) (Result, error) {
	return load(ctx, q, clubID, transactionID, createdAt)
}

func load(ctx context.Context, q *store.Queries, clubID, txID int64, createdAt time.Time) (Result, error) {
	t, err := q.GetLedgerTransaction(ctx, store.GetLedgerTransactionParams{ClubID: clubID, ID: txID, CreatedAt: createdAt})
	if err != nil {
		return Result{}, fmt.Errorf("ledger: load transaction %d: %w", txID, err)
	}
	res := Result{
		TransactionID: t.ID, CreatedAt: t.CreatedAt, Kind: Kind(t.Kind), Meta: t.Meta,
		Refs: Refs{
			CashShiftID: deref(t.CashShiftID), CashTransactionID: deref(t.CashTransactionID),
			ShopOrderID: deref(t.ShopOrderID), ReversesTransactionID: deref(t.ReversesTransactionID),
		},
	}
	if t.SessionID != nil {
		res.Refs.SessionID = *t.SessionID
	}
	entries, err := q.ListLedgerEntriesByTransaction(ctx, store.ListLedgerEntriesByTransactionParams{
		ClubID: clubID, TransactionID: txID, CreatedAt: createdAt,
	})
	if err != nil {
		return Result{}, fmt.Errorf("ledger: load entries: %w", err)
	}
	for _, e := range entries {
		pe := PostedEntry{Account: Account{Kind: AccountKind(e.AccountKind), UserID: e.UserID}, Amount: money.Tiyn(e.Amount)}
		if e.BalanceAfter != nil {
			pe.BalanceAfter = money.Tiyn(*e.BalanceAfter)
		}
		res.Entries = append(res.Entries, pe)
	}
	mins, err := q.ListLedgerMinuteEntriesByTransaction(ctx, store.ListLedgerMinuteEntriesByTransactionParams{
		ClubID: clubID, TransactionID: txID, CreatedAt: createdAt,
	})
	if err != nil {
		return Result{}, fmt.Errorf("ledger: load minute entries: %w", err)
	}
	for _, m := range mins {
		res.Minutes = append(res.Minutes, MinuteEntry{UserID: m.UserID, TimePackageID: m.TimePackageID, TariffID: m.TariffID, Minutes: m.Minutes})
	}
	return res, nil
}

func checkHeader(clubID int64, kind Kind, key string) error {
	switch {
	case clubID <= 0:
		return fmt.Errorf("%w: club id is required", ErrInvalidOperation)
	case !kind.Valid():
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidOperation, kind)
	case strings.TrimSpace(key) == "" || len(key) > MaxIdempotencyKeyLen:
		return fmt.Errorf("%w: idempotency key must be 1..%d bytes", ErrInvalidOperation, MaxIdempotencyKeyLen)
	}
	return nil
}

func checkTenant(ctx context.Context, q *store.Queries, clubID int64) error {
	current, err := q.CurrentTenantClubID(ctx)
	if err != nil {
		// NULL (не тенантная транзакция) не сканируется в int64.
		return fmt.Errorf("%w: %w", ErrTenantMismatch, err)
	}
	if current != clubID {
		return ErrTenantMismatch
	}
	return nil
}

func validate(op Operation) error {
	if err := checkHeader(op.ClubID, op.Kind, op.IdempotencyKey); err != nil {
		return err
	}
	if len(op.Entries) == 0 && len(op.Minutes) == 0 {
		return fmt.Errorf("%w: no entries", ErrInvalidOperation)
	}
	if len(op.Meta) > 0 && !json.Valid(op.Meta) {
		return fmt.Errorf("%w: meta is not valid JSON", ErrInvalidOperation)
	}
	seen := make(map[Account]bool, len(op.Entries))
	var sum money.Tiyn
	for _, e := range op.Entries {
		if !e.Account.valid() {
			return fmt.Errorf("%w: bad account %+v", ErrInvalidOperation, e.Account)
		}
		if e.Amount == 0 || !e.Amount.Valid() {
			return fmt.Errorf("%w: amount of %s must be non-zero and within range", ErrInvalidOperation, e.Account)
		}
		if seen[e.Account] {
			return fmt.Errorf("%w: account %s appears twice", ErrInvalidOperation, e.Account)
		}
		seen[e.Account] = true
		sum += e.Amount
	}
	if sum != 0 {
		return ErrUnbalanced
	}
	for _, m := range op.Minutes {
		if m.UserID <= 0 || m.Minutes == 0 || m.TimePackageID < 0 || m.TariffID < 0 {
			return fmt.Errorf("%w: bad minute entry %+v", ErrInvalidOperation, m)
		}
	}
	return nil
}

// checkBalances — дебет не уводит баланс клиента в минус (кроме входящего остатка переноса, Q-004),
// кредит не превышает MaxMemberBalance. То же проверяет триггер БД; здесь — чтобы вернуть понятную ошибку.
func checkBalances(op Operation, b Balances) error {
	for _, e := range op.Entries {
		if !e.Account.Kind.Member() {
			continue
		}
		before := b.Of(e.Account)
		after := before + e.Amount
		if e.Amount < 0 && after < 0 && op.Kind != KindMigrationOpeningBalance {
			return &InsufficientFundsError{Account: e.Account, Balance: before, Debit: -e.Amount}
		}
		if e.Amount > 0 && after > MaxMemberBalance {
			return fmt.Errorf("%w: %s would be %s", ErrBalanceOverflow, e.Account, after)
		}
	}
	return nil
}

func memberUsers(entries []Entry) []int64 {
	var ids []int64
	for _, e := range entries {
		if e.Account.Kind.Member() {
			ids = append(ids, e.Account.UserID)
		}
	}
	return ids
}

// upsertAccounts создаёт недостающие счета и возвращает их id. Счёт, созданный параллельной транзакцией,
// снимок запроса может не увидеть — тогда запрос повторяется (новый снимок).
func upsertAccounts(ctx context.Context, q *store.Queries, clubID int64, entries []Entry) (map[Account]int64, error) {
	want := make(map[Account]bool, len(entries))
	for _, e := range entries {
		want[e.Account] = true
	}
	ids := make(map[Account]int64, len(want))
	for attempt := 0; attempt < 3 && len(ids) < len(want); attempt++ {
		var kinds []string
		var users []int64
		for a := range want {
			if _, ok := ids[a]; !ok {
				kinds = append(kinds, string(a.Kind))
				users = append(users, a.UserID)
			}
		}
		rows, err := q.UpsertLedgerAccounts(ctx, store.UpsertLedgerAccountsParams{ClubID: clubID, Kinds: kinds, UserIds: users})
		if err != nil {
			return nil, mapDBError(err)
		}
		for _, r := range rows {
			ids[Account{Kind: AccountKind(r.Kind), UserID: r.UserID}] = r.ID
		}
	}
	if len(ids) < len(want) {
		return nil, errors.New("ledger: could not resolve accounts")
	}
	return ids, nil
}

// mapDBError переводит ошибки триггеров 0002 в ошибки пакета.
func mapDBError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.ConstraintName {
		case "ledger_member_balance_non_negative":
			return fmt.Errorf("%w: %s", ErrInsufficientFunds, pgErr.Message)
		case "ledger_member_balance_exists":
			return fmt.Errorf("%w: %s", ErrBalanceNotFound, pgErr.Message)
		case "ledger_entries_balanced":
			return fmt.Errorf("%w: %s", ErrUnbalanced, pgErr.Message)
		}
	}
	return fmt.Errorf("ledger: %w", err)
}

// fingerprint — sha256 канонического JSON {kind, request}: ключи объектов отсортированы, числа как есть.
func fingerprint(kind Kind, request any) ([]byte, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("%w: request is not serializable: %w", ErrInvalidOperation, err)
	}
	canon, err := canonicalJSON(raw)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(struct {
		Kind    Kind            `json:"kind"`
		Request json.RawMessage `json:"request"`
	}{kind, canon})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	return sum[:], nil
}

func canonicalJSON(raw []byte) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage("null"), nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("%w: invalid JSON: %w", ErrInvalidOperation, err)
	}
	out, err := json.Marshal(v) // map → ключи по алфавиту
	if err != nil {
		return nil, err
	}
	return out, nil
}

type fpEntry struct {
	Account string `json:"account"`
	Amount  int64  `json:"amount"`
}

// staticRequest — тело для отпечатка операции с готовыми проводками (порядок проводок не важен).
func staticRequest(op Operation) any {
	entries := make([]fpEntry, len(op.Entries))
	for i, e := range op.Entries {
		entries[i] = fpEntry{Account: e.Account.String(), Amount: int64(e.Amount)}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Account < entries[j].Account })
	minutes := slices.Clone(op.Minutes)
	sort.Slice(minutes, func(i, j int) bool {
		a, b := minutes[i], minutes[j]
		if a.UserID != b.UserID {
			return a.UserID < b.UserID
		}
		if a.TimePackageID != b.TimePackageID {
			return a.TimePackageID < b.TimePackageID
		}
		return a.Minutes < b.Minutes
	})
	meta := op.Meta
	if len(meta) == 0 {
		meta = json.RawMessage("null")
	}
	return struct {
		Entries []fpEntry       `json:"entries"`
		Minutes []MinuteEntry   `json:"minutes"`
		Meta    json.RawMessage `json:"meta"`
		Refs    Refs            `json:"refs"`
	}{entries, minutes, meta, op.Refs}
}

func nonZero(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
