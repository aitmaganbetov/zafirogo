package customers

// Создание и изменение клиента, сброс пароля. Laravel: app/Http/Controllers/UserController.php store(), update(),
// resetPassword(); app/Http/Requests/StoreUserRequest.php.

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	customersapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/customers"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Тексты UserController (ключи перевода).
const (
	msgUserAdded          = "Пользователь добавлен."
	msgUserCreatedLog     = "Создан пользователь: :user"
	msgCustomerSaved      = "Данные клиента сохранены."
	msgCustomerUpdatedLog = "Изменён клиент: :user"
	msgDiscountByManager  = "Скидку клиента задаёт менеджер."
	msgLockedAccount      = "Это сотрудник другого клуба: имя и email меняет он сам. Здесь меняются только скидка и баланс."
	msgLockedPassword     = "Это сотрудник другого клуба: пароль он восстанавливает сам («Забыли пароль?»)." // #nosec G101 -- текст для пользователя, не секрет
	msgStaffElsewhere     = "Сотрудников меняют на странице «Работники»."
	msgPasswordResetLog   = "Сброшен пароль клиента :user" // #nosec G101 -- текст журнала, не секрет
	// msgGuestSignedOut — причина выхода на ПК (SignOutGuest) после сброса пароля сотрудником.
	msgGuestSignedOut = "Пароль изменён на другом устройстве. Войдите с новым паролем." // #nosec G101 -- текст для пользователя, не секрет
)

const (
	maxString   = 255
	minPassword = 6
	// subjectUser — морф-класс пользователя (PD-14).
	subjectUser = billingsvc.SubjectUser
)

var phoneRe = regexp.MustCompile(`^\+7\d{10}$`)

// NormalizePhone — PhoneNumber::normalize() (app/Support/PhoneNumber.php, решение Q-033): цифры; 11 цифр с ведущей
// 7 или 8 → без неё; "+7" + цифры. Пусто — "". (StoreUserRequest::prepareForValidation() отрезал только 7.)
func NormalizePhone(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	d := b.String()
	if d == "" {
		return ""
	}
	if len(d) == 11 && (d[0] == '7' || d[0] == '8') {
		d = d[1:]
	}
	return "+7" + d
}

// validEmail — правило email Laravel (RFC): адрес без имени и угловых скобок.
func validEmail(s string) bool {
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s && a.Name == ""
}

// trimmed — TrimStrings + ConvertEmptyStringsToNull Laravel: nil или пустая строка после обрезки — nil.
func trimmed(s *string) *string {
	if s == nil {
		return nil
	}
	v := strings.TrimSpace(*s)
	if v == "" {
		return nil
	}
	return &v
}

// checkDiscount — 'discount' => nullable|numeric|min:0|max:100 (значение уже не пустое).
func checkDiscount(f *httpx.FormErrors, d string) {
	if !money.PHPIsNumeric(d) {
		f.AddErr(httpx.RuleError("discount", cash.RuleNumeric))
		return
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(d), 64)
	switch {
	case err != nil:
		f.AddErr(httpx.RuleError("discount", cash.RuleNumeric))
	case v < 0:
		f.AddErr(httpx.RuleErrorWith("discount", cash.RuleMin, "0"))
	case v > 100:
		f.AddErr(httpx.RuleErrorWith("discount", cash.RuleMax, "100"))
	}
}

// checkName — 'name' => required|string|max:255.
func checkName(f *httpx.FormErrors, name string) {
	switch {
	case name == "":
		f.AddErr(httpx.RuleError("name", cash.RuleRequired))
	case utf8.RuneCountInString(name) > maxString:
		f.AddErr(httpx.RuleErrorWith("name", "max_string", strconv.Itoa(maxString)))
	}
}

// checkEmail — 'email' => nullable|email|max:255 (уникальность — отдельно).
func checkEmail(f *httpx.FormErrors, email *string) {
	if email == nil {
		return
	}
	switch {
	case !validEmail(*email):
		f.AddErr(httpx.RuleError("email", "email"))
	case utf8.RuneCountInString(*email) > maxString:
		f.AddErr(httpx.RuleErrorWith("email", "max_string", strconv.Itoa(maxString)))
	}
}

// conflicts — Rule::unique('users', 'phone'/'email') (users — платформенная таблица, без RLS).
func conflicts(ctx context.Context, q *store.Queries, phone string, email *string, exceptUserID int64) (store.AdminUserConflictsRow, error) {
	e := ""
	if email != nil {
		e = *email
	}
	return q.AdminUserConflicts(ctx, store.AdminUserConflictsParams{Phone: phone, Email: e, ExceptUserID: exceptUserID})
}

// uniqueViolation — нарушение уникальности users_phone_unique (гонка двух созданий одного номера).
func uniqueViolation(err error, constraint string) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == constraint
}

// audit — запись audit_log (категория permission/security), без секретов.
func audit(ctx context.Context, q *store.Queries, clubID int64, category, action, subjectType string, subjectID int64, before, after any) error {
	a := httpx.Actor(ctx)
	p := store.InsertAuditLogParams{ClubID: &clubID, ActorKind: a.Kind, Category: category, Action: action}
	if subjectType != "" {
		p.SubjectType, p.SubjectID = &subjectType, &subjectID
	}
	if a.UserID > 0 {
		p.ActorUserID = &a.UserID
	}
	if a.IP.IsValid() {
		ip := a.IP
		p.Ip = &ip
	}
	if a.RequestID != "" {
		p.RequestID = &a.RequestID
	}
	var err error
	if before != nil {
		if p.Before, err = json.Marshal(before); err != nil {
			return err
		}
	}
	if after != nil {
		if p.After, err = json.Marshal(after); err != nil {
			return err
		}
	}
	return q.InsertAuditLog(ctx, p)
}

// customerOrStaff — UserController::customer(): клиент клуба; сотрудник — 403 «Сотрудников меняют на странице
// «Работники».», нет членства — 404.
func customerOrStaff(ctx context.Context, q *store.Queries, clubID, userID int64) (store.AdminGetCustomerRow, error) {
	row, err := q.AdminGetCustomer(ctx, store.AdminGetCustomerParams{ClubID: clubID, UserID: userID})
	if !errors.Is(err, pgx.ErrNoRows) {
		return row, err
	}
	m, err := q.GetMembershipAuth(ctx, store.GetMembershipAuthParams{ClubID: clubID, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return row, httpx.NotFound()
	}
	if err != nil {
		return row, err
	}
	if permissions.IsStaff(m.Role) {
		return row, httpx.NewAPIError(http.StatusForbidden, permissions.CodeForbidden, msgStaffElsewhere)
	}
	return row, httpx.NotFound()
}

// isLocked — UserController::isAccountLocked().
func (h *handlers) isLocked(ctx context.Context, userID int64) (bool, error) {
	ids, err := h.accountsLocked(ctx, []int64{userID})
	return len(ids) > 0, err
}

// createFingerprint — тело запроса для ключа идемпотентности без паролей.
func createFingerprint(b *customersapi.CustomerCreateRequest) map[string]any {
	return map[string]any{
		"name": b.Name, "phone": b.Phone, "email": b.Email, "discount": b.Discount,
		"balance": b.BalanceTiyn, "bonus_balance": b.BonusBalanceTiyn,
	}
}

func orZero(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

// CreateCustomer — UserController::store() + StoreUserRequest.
func (h *handlers) CreateCustomer(ctx context.Context, req customersapi.CreateCustomerRequestObject) (customersapi.CreateCustomerResponseObject, error) {
	if h.d.Passwords == nil {
		return nil, httpx.ErrNotConfigured
	}
	b := req.Body
	clubID, key := httpx.ClubID(ctx), req.Params.IdempotencyKey
	name := strings.TrimSpace(b.Name)
	phone := NormalizePhone(b.Phone)
	email := trimmed(b.Email)
	discount := trimmed(b.Discount)
	balance, bonusBalance := orZero(b.BalanceTiyn), orZero(b.BonusBalanceTiyn)

	var form httpx.FormErrors
	checkName(&form, name)
	switch {
	case phone == "":
		form.AddErr(httpx.RuleError("phone", cash.RuleRequired))
	case !phoneRe.MatchString(phone):
		form.AddErr(httpx.RuleError("phone", "regex"))
	}
	checkEmail(&form, email)
	switch {
	case b.Password == "":
		form.AddErr(httpx.RuleError("password", cash.RuleRequired))
	case utf8.RuneCountInString(b.Password) < minPassword:
		form.AddErr(httpx.RuleErrorWith("password", "min_string", strconv.Itoa(minPassword)))
	case b.Password != b.PasswordConfirmation:
		form.AddErr(httpx.RuleError("password", "confirmed"))
	}
	if discount != nil {
		checkDiscount(&form, *discount)
	}
	checkInitialBalance(&form, "balance", balance)
	checkInitialBalance(&form, "bonus_balance", bonusBalance)

	// Уникальность телефона и email — до остальных проверок в БД (правила формы).
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		p := phone
		if form.Has("phone") {
			p = ""
		}
		var e *string
		if !form.Has("email") {
			e = email
		}
		c, err := conflicts(ctx, q, p, e, 0)
		if err != nil {
			return err
		}
		if c.PhoneTaken {
			form.AddErr(httpx.RuleError("phone", "unique"))
		}
		if c.EmailTaken {
			form.AddErr(httpx.RuleError("email", "unique"))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := form.Err(); err != nil {
		return nil, err
	}
	discountValue := "0"
	if discount != nil {
		discountValue = *discount
	}
	// Скидку и начальный баланс задаёт право customer_money.update (UserController::canSetMoney()).
	if !httpx.Principal(ctx).Can("customer_money.update") {
		d, _ := strconv.ParseFloat(discountValue, 64)
		if d != 0 || balance != 0 || bonusBalance != 0 {
			return nil, httpx.NewAPIError(http.StatusForbidden, permissions.CodeForbidden, msgMoneyByManager)
		}
		discountValue = "0"
	}
	hash, err := h.d.Passwords.Hash(ctx, b.Password)
	if err != nil {
		return nil, err
	}
	now := h.d.NowFunc()
	var out customersapi.CustomerSaved
	err = h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		out, err = httpx.Idempotent(ctx, q, clubID, key, "create_customer", createFingerprint(b), now,
			func() (customersapi.CustomerSaved, error) {
				userID, err := q.AdminCreateCustomer(ctx, store.AdminCreateCustomerParams{
					ClubID: clubID, Discount: discountValue, Now: &now, Name: name, Phone: phone, Email: email, PasswordHash: hash,
				})
				if uniqueViolation(err, "users_phone_unique") {
					return customersapi.CustomerSaved{}, httpx.RuleError("phone", "unique")
				}
				if err != nil {
					return customersapi.CustomerSaved{}, err
				}
				if err := q.EnsureMemberBalance(ctx, store.EnsureMemberBalanceParams{ClubID: clubID, UserID: userID}); err != nil {
					return customersapi.CustomerSaved{}, err
				}
				// RQ-13: начальный баланс — операция ledger initial_balance (не UPDATE баланса).
				if _, err := billing.SetInitialBalance(ctx, q, billing.InitialBalanceParams{
					ClubID: clubID, UserID: userID, Main: money.Tiyn(balance), Bonus: money.Tiyn(bonusBalance), Actor: httpx.Actor(ctx),
				}); err != nil {
					return customersapi.CustomerSaved{}, err
				}
				if err := billingsvc.RecordActivity(ctx, q, activity.Entry{
					ClubID: clubID, UserID: httpx.Principal(ctx).UserID, SubjectType: subjectUser, SubjectID: userID,
					Event: "user.created", Description: h.d.T(ctx, msgUserCreatedLog, map[string]string{"user": name}),
					Details: map[string]any{"phone": phone, "role": permissions.RoleClient},
				}); err != nil {
					return customersapi.CustomerSaved{}, err
				}
				row, err := customer(ctx, q, clubID, userID)
				if err != nil {
					return customersapi.CustomerSaved{}, err
				}
				if balance != 0 || bonusBalance != 0 {
					if row, err = h.notifyBalance(ctx, q, clubID, userID); err != nil {
						return customersapi.CustomerSaved{}, err
					}
				}
				view, err := h.view(ctx, q, clubID, row)
				return customersapi.CustomerSaved{Customer: view, Message: h.d.T(ctx, msgUserAdded, nil)}, err
			})
		return err
	})
	if err != nil {
		return nil, err
	}
	return customersapi.CreateCustomer201JSONResponse(out), nil
}

// orDash — ($value ?? '—') журнала изменений.
func orDash(s *string) string {
	if s == nil {
		return "—"
	}
	return *s
}

// UpdateCustomer — UserController::update(): имя, email, скидка и (владелец) пополнение/списание без кассы.
func (h *handlers) UpdateCustomer(ctx context.Context, req customersapi.UpdateCustomerRequestObject) (customersapi.UpdateCustomerResponseObject, error) {
	b := req.Body
	p := httpx.Principal(ctx)
	clubID := httpx.ClubID(ctx)
	canMoney, isOwner := p.Can("customer_money.update"), p.Role == permissions.RoleOwner

	// UserController::customer(): сотрудник — 403, чужой — 404 (до проверки формы).
	var current store.AdminGetCustomerRow
	if err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		current, err = customerOrStaff(ctx, q, clubID, req.UserId)
		return err
	}); err != nil {
		return nil, err
	}
	locked, err := h.isLocked(ctx, req.UserId)
	if err != nil {
		return nil, err
	}

	name := strings.TrimSpace(b.Name)
	email := trimmed(b.Email)
	discount := trimmed(b.Discount)
	balanceType, action := trimmed(b.TopUpBalance), trimmed(b.BalanceAction)
	var form httpx.FormErrors
	checkName(&form, name)
	checkEmail(&form, email)
	if discount != nil {
		if canMoney {
			checkDiscount(&form, *discount)
		} else {
			form.Add("discount", msgDiscountByManager)
		}
	}
	var adj *ownerAdjustment
	if b.TopUpAmountTiyn != nil {
		if !isOwner {
			form.Add("top_up_amount", msgOwnerOnlyBalance)
		} else {
			bt, act := "", "top_up"
			if balanceType != nil {
				bt = *balanceType
			}
			if action != nil {
				act = *action
			}
			if balanceType == nil {
				form.AddErr(httpx.RuleError("top_up_balance", cash.RuleRequired)) // required_with:top_up_amount
			}
			a := parseOwnerAdjustment(&form, *b.TopUpAmountTiyn, bt, act)
			adj = &a
		}
	} else if balanceType != nil && *balanceType != string(billing.FieldMain) && *balanceType != string(billing.FieldBonus) {
		form.AddErr(httpx.RuleError("top_up_balance", cash.RuleIn))
	}
	if action != nil && !isOwner {
		form.Add("balance_action", msgOwnerOnlyBalance)
	}
	if !form.Has("email") && email != nil {
		if err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
			c, err := conflicts(ctx, q, "", email, req.UserId)
			if err == nil && c.EmailTaken {
				form.AddErr(httpx.RuleError("email", "unique"))
			}
			return err
		}); err != nil {
			return nil, err
		}
	}
	if err := form.Err(); err != nil {
		return nil, err
	}
	// Сотрудник другого клуба: имя и email — его аккаунта, здесь меняются только скидка и баланс.
	if locked && (name != current.Name || orDash(email) != orDash(current.Email)) {
		return nil, httpx.Validation("name", msgLockedAccount)
	}
	key := ""
	if req.Params.IdempotencyKey != nil {
		key = strings.TrimSpace(*req.Params.IdempotencyKey)
	}
	if adj != nil && key == "" {
		return nil, httpx.NewAPIError(http.StatusBadRequest, "idempotency_key_required", "")
	}

	now := h.d.NowFunc()
	run := func(ctx context.Context, q *store.Queries) (customersapi.CustomerSaved, error) {
		c, err := customerOrStaff(ctx, q, clubID, req.UserId)
		if err != nil {
			return customersapi.CustomerSaved{}, err
		}
		if _, err := q.AdminUpdateCustomerAccount(ctx, store.AdminUpdateCustomerAccountParams{
			Name: name, Email: email, Now: &now, UserID: req.UserId, ClubID: clubID,
		}); err != nil {
			return customersapi.CustomerSaved{}, err
		}
		after := c.Discount
		if discount != nil {
			if after, err = q.AdminUpdateCustomerDiscount(ctx, store.AdminUpdateCustomerDiscountParams{
				Discount: *discount, Now: &now, ClubID: clubID, UserID: req.UserId,
			}); err != nil {
				return customersapi.CustomerSaved{}, err
			}
		}
		changes := map[string]any{}
		if c.Name != name {
			changes["name"] = c.Name + " → " + name
		}
		if orDash(c.Email) != orDash(email) {
			changes["email"] = orDash(c.Email) + " → " + orDash(email)
		}
		if c.Discount != after {
			changes["discount"] = c.Discount + " → " + after
		}
		if len(changes) > 0 {
			if err := billingsvc.RecordActivity(ctx, q, activity.Entry{
				ClubID: clubID, UserID: p.UserID, SubjectType: subjectUser, SubjectID: req.UserId,
				Event: "user.updated", Description: h.d.T(ctx, msgCustomerUpdatedLog, map[string]string{"user": name}), Details: changes,
			}); err != nil {
				return customersapi.CustomerSaved{}, err
			}
		}
		msg, params := msgCustomerSaved, map[string]string(nil)
		if adj != nil {
			c.Name = name
			if msg, err = h.ownerAdjust(ctx, q, clubID, c, *adj, key); err != nil {
				return customersapi.CustomerSaved{}, err
			}
			params = map[string]string{"amount": httpx.NumberFormat(adj.amount, ".")}
			if _, err := h.notifyBalance(ctx, q, clubID, req.UserId); err != nil {
				return customersapi.CustomerSaved{}, err
			}
		}
		row, err := customer(ctx, q, clubID, req.UserId)
		if err != nil {
			return customersapi.CustomerSaved{}, err
		}
		view, err := h.view(ctx, q, clubID, row)
		return customersapi.CustomerSaved{Customer: view, Message: h.d.T(ctx, msg, params)}, err
	}
	var out customersapi.CustomerSaved
	err = h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		if adj == nil {
			out, err = run(ctx, q)
			return err
		}
		out, err = httpx.Idempotent(ctx, q, clubID, key, "update_customer", map[string]any{"user_id": req.UserId, "body": b}, now,
			func() (customersapi.CustomerSaved, error) { return run(ctx, q) })
		return err
	})
	if err != nil {
		return nil, err
	}
	return customersapi.UpdateCustomer200JSONResponse(out), nil
}

// passwordChars — Str::password(8, symbols: false): буквы обоих регистров и цифры.
const (
	passwordLetters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	passwordDigits  = "0123456789"
	passwordLength  = 8
)

func randomFrom(set string) (byte, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(set))))
	if err != nil {
		return 0, err
	}
	return set[n.Int64()], nil
}

// NewPassword — Str::password(8, letters, numbers, symbols: false): хотя бы одна буква и одна цифра
// (как Laravel — по символу из каждого набора, остальное — из всех, затем перемешать).
func NewPassword() (string, error) {
	all := passwordLetters + passwordDigits
	out := make([]byte, 0, passwordLength)
	for _, set := range []string{passwordLetters, passwordDigits} {
		c, err := randomFrom(set)
		if err != nil {
			return "", err
		}
		out = append(out, c)
	}
	for len(out) < passwordLength {
		c, err := randomFrom(all)
		if err != nil {
			return "", err
		}
		out = append(out, c)
	}
	for i := len(out) - 1; i > 0; i-- {
		j, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return "", err
		}
		out[i], out[j.Int64()] = out[j.Int64()], out[i]
	}
	return string(out), nil
}

// ResetCustomerPassword — UserController::resetPassword(): новый пароль на экран (один раз), клиент выходит на всех ПК.
func (h *handlers) ResetCustomerPassword(ctx context.Context, req customersapi.ResetCustomerPasswordRequestObject) (customersapi.ResetCustomerPasswordResponseObject, error) {
	if h.d.Passwords == nil {
		return nil, httpx.ErrNotConfigured
	}
	clubID := httpx.ClubID(ctx)
	var c store.AdminGetCustomerRow
	if err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		c, err = customerOrStaff(ctx, q, clubID, req.UserId)
		return err
	}); err != nil {
		return nil, err
	}
	locked, err := h.isLocked(ctx, req.UserId)
	if err != nil {
		return nil, err
	}
	// Пароль сотрудника открывает панель другого клуба: сотрудникам этого клуба он не показывается.
	if locked {
		return nil, httpx.NewAPIError(http.StatusForbidden, permissions.CodeForbidden, msgLockedPassword)
	}
	pw, err := NewPassword()
	if err != nil {
		return nil, err
	}
	hash, err := h.d.Passwords.Hash(ctx, pw)
	if err != nil {
		return nil, err
	}
	now := h.d.NowFunc()
	err = h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		n, err := q.AdminSetCustomerPassword(ctx, store.AdminSetCustomerPasswordParams{PasswordHash: hash, Now: &now, UserID: req.UserId, ClubID: clubID})
		if err != nil {
			return err
		}
		if n == 0 {
			return httpx.NotFound()
		}
		if err := billingsvc.RecordActivity(ctx, q, activity.Entry{
			ClubID: clubID, UserID: httpx.Principal(ctx).UserID, SubjectType: subjectUser, SubjectID: req.UserId,
			Event: "user.password_reset", Description: h.d.T(ctx, msgPasswordResetLog, map[string]string{"user": c.Name}),
			Details: map[string]any{"delivery": "screen"},
		}); err != nil {
			return err
		}
		if err := audit(ctx, q, clubID, "security", "user.password_reset", subjectUser, req.UserId, nil, map[string]any{"delivery": "screen"}); err != nil {
			return err
		}
		// $user->tokens()->delete(): токены гостя на ПК отзываются, ПК клуба получают SignOutGuest.
		if h.d.SignOutGuest != nil {
			signOut, userID := h.d.SignOutGuest, req.UserId
			billingsvc.AfterCommit(ctx, func(ctx context.Context) { signOut(ctx, clubID, userID, msgGuestSignedOut) })
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return customersapi.ResetCustomerPassword200JSONResponse{
		Body:    customersapi.CustomerPasswordReset{UserId: req.UserId, Name: c.Name, Password: pw},
		Headers: customersapi.ResetCustomerPassword200ResponseHeaders{CacheControl: httpx.Ptr("no-store, private")},
	}, nil
}
