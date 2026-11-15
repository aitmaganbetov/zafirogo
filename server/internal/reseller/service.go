package reseller

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Тексты старого кода (ключи перевода). Laravel: Superadmin\ResellerController, ResellerController,
// SettingsController::reseller(), AuthController::login().
const (
	MsgCodeFormat      = "Код — от 3 до 32 символов: латинские буквы, цифры и дефис, начинается с буквы или цифры."
	MsgCodeTaken       = "Этот код уже занят — выберите другой."
	MsgPhoneInvalid    = "Введите корректный номер телефона."
	MsgAlreadyReseller = "Аккаунт с этим телефоном уже реселлер."
	MsgNotReseller     = "Этот аккаунт не реселлер Zafiro."
	MsgResellersOnly   = "Здесь входят только реселлеры Zafiro."
	MsgAlreadyAttached = "Клуб уже привязан к реселлеру. Сменить реселлера можно через поддержку Zafiro."
	MsgCodeNotFound    = "Реселлер с таким кодом не найден."
)

// Ошибки сервиса. Обработчики превращают их в ответы: ErrNotFound — 404, ErrNotReseller — 403, остальные — ошибка
// поля формы.
var (
	// ErrNotFound — реселлера нет (или клуб не этого реселлера).
	ErrNotFound = errors.New("reseller: не найден")
	// ErrNotReseller — у аккаунта нет записи реселлера: кабинет не его.
	ErrNotReseller = errors.New("reseller: аккаунт не реселлер")
	// ErrCodeTaken — код уже занят (Rule::unique('resellers', 'code')).
	ErrCodeTaken = errors.New("reseller: код занят")
	// ErrPasswordRequired — аккаунта с этим телефоном нет, а пароль не задан.
	ErrPasswordRequired = errors.New("reseller: новому аккаунту нужен пароль")
	// ErrAlreadyReseller — аккаунт с этим телефоном уже реселлер.
	ErrAlreadyReseller = errors.New("reseller: аккаунт уже реселлер")
	// ErrAlreadyAttached — клуб уже привязан к реселлеру (код вводится один раз).
	ErrAlreadyAttached = errors.New("reseller: клуб уже привязан")
	// ErrCodeNotFound — действующего реселлера с таким кодом нет (в том числе остановленный код).
	ErrCodeNotFound = errors.New("reseller: код не найден")
	// ErrIdempotencyConflict — ключ идемпотентности выплаты уже использован с другими данными.
	ErrIdempotencyConflict = errors.New("reseller: ключ идемпотентности использован с другими данными")
)

const (
	codeUniqueIndex   = "resellers_code_unique"
	userUniqueIndex   = "resellers_user_id_unique"
	payoutUniqueIndex = "reseller_payouts_idempotency_key_unique"
)

func uniqueViolation(err error, constraint string) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == constraint
}

// CreateParams — форма «Новый реселлер». Поля уже нормализованы и проверены по формату (NormalizeCode, ValidCode,
// NormalizePhone, ValidPhone, пределы).
type CreateParams struct {
	Name  string
	Phone string
	Code  string
	// Password — пароль нового аккаунта; пустой — не задан (существующий аккаунт сохраняет свой).
	Password        string
	CommissionBP    int
	DiscountPercent int
	// Hash — хэш пароля нового аккаунта (Argon2id); вызывается, только если аккаунта с этим телефоном нет.
	Hash func(ctx context.Context, password string) (string, error)
	Now  time.Time
}

// Created — созданный реселлер.
type Created struct {
	ID     int64
	UserID int64
	Code   string
	Name   string
	// ExistingAccount — реселлером стал уже существовавший аккаунт (пароль прежний).
	ExistingAccount bool
}

// Create — суперадмин создаёт реселлера: новый аккаунт (телефон и пароль) или существующий по телефону — он сохраняет
// свой пароль и становится реселлером один раз. Ошибки формы — ErrCodeTaken и ErrPasswordRequired (обе сразу, как
// $request->validate()), затем ErrAlreadyReseller.
// Laravel: app/Http/Controllers/Superadmin/ResellerController.php ResellerController::store().
func Create(ctx context.Context, q *store.Queries, p CreateParams) (Created, error) {
	out := Created{Code: p.Code, Name: p.Name}
	// $existing = User::query()->where('phone', …)->first()
	user, err := q.ResellerUserByPhone(ctx, p.Phone)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return out, err
	default:
		out.UserID, out.ExistingAccount = user.ID, true
	}
	var invalid []error
	// 'password' => [$existing === null ? 'required' : 'nullable', …]
	if !out.ExistingAccount && p.Password == "" {
		invalid = append(invalid, ErrPasswordRequired)
	}
	taken, err := q.ResellerCodeTaken(ctx, p.Code)
	if err != nil {
		return out, err
	}
	if taken {
		invalid = append(invalid, ErrCodeTaken)
	}
	if len(invalid) > 0 {
		return out, errors.Join(invalid...)
	}
	if out.ExistingAccount {
		already, err := q.ResellerUserIsReseller(ctx, out.UserID)
		if err != nil {
			return out, err
		}
		if already {
			return out, ErrAlreadyReseller
		}
	} else {
		hash, err := p.Hash(ctx, p.Password)
		if err != nil {
			return out, err
		}
		// User::query()->create(['name' => …, 'phone' => …, 'password' => Hash::make(…)])
		if out.UserID, err = q.ResellerCreateUser(ctx, store.ResellerCreateUserParams{Name: p.Name, Phone: p.Phone, Password: hash, Now: &p.Now}); err != nil {
			return out, err
		}
	}
	out.ID, err = q.ResellerInsert(ctx, store.ResellerInsertParams{
		UserID: out.UserID, Code: p.Code, Name: p.Name,
		CommissionBp: int32(p.CommissionBP), DiscountPercent: int16(p.DiscountPercent), // #nosec G115 -- 0…10000 и 0…100, проверено формой и CHECK
		Now: &p.Now,
	})
	// Гонка двух созданий: второй упирается в уникальный индекс.
	switch {
	case uniqueViolation(err, codeUniqueIndex):
		return out, ErrCodeTaken
	case uniqueViolation(err, userUniqueIndex):
		return out, ErrAlreadyReseller
	}
	return out, err
}

// Terms — условия реселлера, которые задаёт суперадмин.
type Terms struct {
	CommissionBP    int
	DiscountPercent int
	// Active — «Код действует»: остановленный реселлер не даёт скидки и процента в новых счетах, клубы не могут
	// ввести его код.
	Active bool
}

// Updated — итог изменения условий: реселлер и что было до.
type Updated struct {
	ID     int64
	Code   string
	Before Terms
	After  Terms
}

// Changed — условия изменились (в старом журнал пишется только при getChanges() !== []).
func (u Updated) Changed() bool { return u.Before != u.After }

// Update — суперадмин задаёт процент реселлера, скидку его клубам и «код действует». На уже выставленные счета не
// влияет: счёт хранит снимок (BR-186).
// Laravel: app/Http/Controllers/Superadmin/ResellerController.php ResellerController::update().
func Update(ctx context.Context, q *store.Queries, id int64, t Terms, now time.Time) (Updated, error) {
	r, err := lock(ctx, q, id)
	if err != nil {
		return Updated{}, err
	}
	out := Updated{ID: r.ID, Code: r.Code, After: t,
		Before: Terms{CommissionBP: int(r.CommissionBp), DiscountPercent: int(r.DiscountPercent), Active: r.IsActive}}
	if !out.Changed() {
		return out, nil
	}
	return out, q.ResellerUpdate(ctx, store.ResellerUpdateParams{
		ID: r.ID, CommissionBp: int32(t.CommissionBP), DiscountPercent: int16(t.DiscountPercent), // #nosec G115 -- 0…10000 и 0…100
		IsActive: t.Active, Now: &now,
	})
}

func lock(ctx context.Context, q *store.Queries, id int64) (store.ResellerLockRow, error) {
	r, err := q.ResellerLock(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// PayoutParams — форма «Записать выплату».
type PayoutParams struct {
	ResellerID int64
	// Amount — сумма в тиынах, MinPayout…MaxPayout. Превышение остатка не проверяется (BR-188).
	Amount int64
	// Note — комментарий («Kaspi, ноябрь»); пусто — нет.
	Note string
	// By — суперадмин, записавший выплату.
	By int64
	// Key — ключ идемпотентности формы: повтор запроса не создаёт вторую выплату (§3.6).
	Key string
	Now time.Time
}

// PaidOut — записанная выплата.
type PaidOut struct {
	ID     int64
	Code   string
	Amount int64
	// Replayed — выплата с этим ключом уже была записана раньше; новая строка не создана.
	Replayed bool
}

// Payout — суперадмин записывает выплату реселлеру: документ reseller_payouts и в той же транзакции строка
// reseller_payout:<id выплаты> журнала денег платформы (только добавление; она остаётся и после удаления реселлера).
// Выплата не исправляется и не удаляется. Повтор с тем же ключом и теми же данными возвращает уже записанную
// выплату; с другими данными — ErrIdempotencyConflict.
// Laravel: app/Http/Controllers/Superadmin/ResellerController.php ResellerController::payout() (ключа там нет:
// двойной клик создавал две выплаты).
func Payout(ctx context.Context, q *store.Queries, p PayoutParams) (PaidOut, error) {
	// Блокировка реселлера упорядочивает повторы одной формы: второй запрос увидит выплату первого.
	r, err := lock(ctx, q, p.ResellerID)
	if err != nil {
		return PaidOut{}, err
	}
	out := PaidOut{Code: r.Code, Amount: p.Amount}
	prev, err := q.ResellerPayoutByKey(ctx, p.Key)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return out, err
	default:
		prevNote := ""
		if prev.Note != nil {
			prevNote = *prev.Note
		}
		if prev.ResellerID != p.ResellerID || prev.Amount != p.Amount || prevNote != p.Note {
			return out, ErrIdempotencyConflict
		}
		out.ID, out.Replayed = prev.ID, true
		return out, nil
	}
	var note *string
	if p.Note != "" {
		note = &p.Note
	}
	out.ID, err = q.ResellerInsertPayout(ctx, store.ResellerInsertPayoutParams{
		ResellerID: r.ID, Amount: p.Amount, Note: note, CreatedBy: &p.By, IdempotencyKey: p.Key, Now: &p.Now})
	if uniqueViolation(err, payoutUniqueIndex) {
		// Ключ занят выплатой другого реселлера, записанной одновременно (под блокировкой своего её не видно).
		return out, ErrIdempotencyConflict
	}
	if err != nil {
		return out, err
	}
	meta := map[string]any{"code": r.Code}
	if p.Note != "" {
		meta["note"] = p.Note
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return out, err
	}
	return out, q.ResellerInsertPayoutMoney(ctx, store.ResellerInsertPayoutMoneyParams{
		IdempotencyKey: "reseller_payout:" + strconv.FormatInt(out.ID, 10), Amount: p.Amount,
		ResellerID: &r.ID, PayoutID: &out.ID, ActorUserID: &p.By, Meta: raw, Now: p.Now})
}

// Detached — клуб, отвязанный от реселлера.
type Detached struct {
	ResellerID int64
	Code       string
	ClubID     int64
	ClubName   string
}

// Detach — суперадмин отвязывает клуб от реселлера: новые счета клуба идут без его скидки и процента; уже
// выставленный неоплаченный счёт хранит снимок (BR-189). Клуб не этого реселлера — ErrNotFound.
// Laravel: app/Http/Controllers/Superadmin/ResellerController.php ResellerController::detach().
func Detach(ctx context.Context, q *store.Queries, resellerID, clubID int64, now time.Time) (Detached, error) {
	r, err := lock(ctx, q, resellerID)
	if err != nil {
		return Detached{}, err
	}
	club, err := q.ResellerLockClub(ctx, clubID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Detached{}, ErrNotFound
	}
	if err != nil {
		return Detached{}, err
	}
	// abort_unless($club->reseller_id === $reseller->getKey(), 404)
	if club.ResellerID == nil || *club.ResellerID != r.ID {
		return Detached{}, ErrNotFound
	}
	return Detached{ResellerID: r.ID, Code: r.Code, ClubID: club.ID, ClubName: club.Name},
		q.ResellerDetachClub(ctx, store.ResellerDetachClubParams{ID: club.ID, ResellerID: r.ID, Now: &now})
}

// Deleted — удалённый реселлер.
type Deleted struct {
	ID     int64
	UserID int64
	Code   string
	Name   string
	// Clubs — сколько клубов отвязано.
	Clubs int64
}

// Delete — суперадмин удаляет реселлера: аккаунт остаётся и больше не реселлер, клубы отвязываются, выплаты
// удаляются каскадом, в счетах reseller_id становится NULL, а начисленное (reseller_amount) остаётся (BR-189).
// Суммы выплат и начислений остаются в журнале денег платформы (UQ-10). Аккаунт можно сделать реселлером снова.
// Laravel: app/Http/Controllers/Superadmin/ResellerController.php ResellerController::destroy().
func Delete(ctx context.Context, q *store.Queries, id int64) (Deleted, error) {
	r, err := lock(ctx, q, id)
	if err != nil {
		return Deleted{}, err
	}
	out := Deleted{ID: r.ID, UserID: r.UserID, Code: r.Code, Name: r.Name}
	if out.Clubs, err = q.ResellerDetachAll(ctx, r.ID); err != nil {
		return out, err
	}
	return out, q.ResellerDelete(ctx, r.ID)
}

// Attached — реселлер, к которому привязан клуб.
type Attached struct {
	ID   int64
	Code string
	Name string
}

// Attach — клуб вводит код своего реселлера один раз: клуб становится клубом реселлера, платит подписку с его
// скидкой, а реселлер получает процент. Сменить или снять реселлера может только суперадмин. Проверки — в порядке
// старого кода: клуб уже привязан — ErrAlreadyAttached; кода нет или он остановлен — ErrCodeNotFound. code — как
// ввёл клуб (нормализуется здесь). Выполняется пулом платформы: clubs.reseller_id пул клуба не меняет (0034).
// Laravel: app/Http/Controllers/SettingsController.php SettingsController::reseller().
func Attach(ctx context.Context, q *store.Queries, clubID int64, code string, now time.Time) (Attached, error) {
	code = NormalizeCode(code)
	club, err := q.ResellerLockClub(ctx, clubID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Attached{}, ErrNotFound
	}
	if err != nil {
		return Attached{}, err
	}
	if club.ResellerID != nil {
		return Attached{}, ErrAlreadyAttached
	}
	if code == "" {
		return Attached{}, ErrCodeNotFound
	}
	r, err := q.ResellerActiveByCode(ctx, code)
	if errors.Is(err, pgx.ErrNoRows) {
		return Attached{}, ErrCodeNotFound
	}
	if err != nil {
		return Attached{}, err
	}
	n, err := q.ResellerAttachClub(ctx, store.ResellerAttachClubParams{ID: clubID, ResellerID: r.ID, Now: &now})
	if err != nil {
		return Attached{}, err
	}
	if n == 0 {
		return Attached{}, ErrAlreadyAttached
	}
	return Attached{ID: r.ID, Code: r.Code, Name: r.Name}, nil
}

// IsReseller — у аккаунта есть запись реселлера (вход на хосте реселлера).
// Laravel: AuthController::login() — Reseller::query()->where('user_id', …)->exists().
func IsReseller(ctx context.Context, q *store.Queries, userID int64) (bool, error) {
	return q.ResellerUserIsReseller(ctx, userID)
}

// Cabinet — кабинет реселлера: его код и условия, клубы, начисления, выплаты и суммы.
type Cabinet struct {
	Code            string
	CommissionBP    int
	DiscountPercent int
	Active          bool
	// Earned — заработано всего; PaidOut — выплачено; Balance — к выплате (тиыны, BR-187).
	Earned  int64
	PaidOut int64
	Balance int64

	Clubs         []store.ResellerCabinetClubsRow
	ClubsTotal    int64
	Earnings      []store.ResellerCabinetEarningsRow
	EarningsTotal int64
	Payouts       []store.ResellerCabinetPayoutsRow
}

// Dashboard — кабинет реселлера аккаунта userID: только его клубы, начисления и выплаты (каждый запрос — по id его
// записи реселлера). Не реселлер — ErrNotReseller. clubsPage, earningsPage — номера страниц с 1.
// Laravel: app/Http/Controllers/ResellerController.php ResellerController::dashboard().
func Dashboard(ctx context.Context, q *store.Queries, userID int64, clubsPage, earningsPage int) (Cabinet, error) {
	r, err := q.ResellerByUser(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Cabinet{}, ErrNotReseller
	}
	if err != nil {
		return Cabinet{}, err
	}
	out := Cabinet{Code: r.Code, CommissionBP: int(r.CommissionBp), DiscountPercent: int(r.DiscountPercent), Active: r.IsActive}
	totals, err := q.ResellerTotals(ctx, r.ID)
	if err != nil {
		return out, err
	}
	out.Earned, out.PaidOut, out.Balance = totals.Earned, totals.PaidOut, Balance(totals.Earned, totals.PaidOut)
	if out.ClubsTotal, err = q.ResellerCabinetClubsCount(ctx, r.ID); err != nil {
		return out, err
	}
	if out.Clubs, err = q.ResellerCabinetClubs(ctx, store.ResellerCabinetClubsParams{
		ResellerID: r.ID, PageSize: ClubsPage, PageOffset: int64(max(clubsPage, 1)-1) * ClubsPage}); err != nil {
		return out, err
	}
	if out.EarningsTotal, err = q.ResellerCabinetEarningsCount(ctx, r.ID); err != nil {
		return out, err
	}
	if out.Earnings, err = q.ResellerCabinetEarnings(ctx, store.ResellerCabinetEarningsParams{
		ResellerID: r.ID, PageSize: EarningsPage, PageOffset: int64(max(earningsPage, 1)-1) * EarningsPage}); err != nil {
		return out, err
	}
	out.Payouts, err = q.ResellerCabinetPayouts(ctx, r.ID)
	return out, err
}
