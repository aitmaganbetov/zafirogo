package superadmin

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	superadminapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/superadmin"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/reseller"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// «Реселлеры»: суперадмин создаёт реселлеров (новый аккаунт или существующий по телефону), задаёт процент и скидку
// клубам, записывает выплаты, отвязывает клубы и удаляет реселлеров. Правила и запись — internal/reseller.
// Laravel: app/Http/Controllers/Superadmin/ResellerController.php, superadmin/resellers/index.blade.php.
//
// Отличия от Laravel (docs/open-questions/upstream-resellers.md):
//   - выплата и удаление реселлера требуют свежего кода TOTP действующего суперадмина (как выдача прав суперадмина,
//     аудит A-10): обе операции необратимы — выплата не исправляется и не удаляется, удаление стирает выплаты;
//   - выплата идёт под ключом идемпотентности формы и пишет строку журнала денег платформы;
//   - в режиме просмотра клуба раздел только читается (http/readonly.go);
//   - каждое изменение пишется ещё и в audit_log.
const (
	subjectReseller = `App\Models\Reseller`
	subjectPayout   = `App\Models\ResellerPayout`

	msgResellerCreated         = "Реселлер :code создан. Он входит на :host со своим телефоном и паролем."
	msgResellerCreatedExisting = "Реселлер :code создан для существующего аккаунта: он входит на :host со своим паролем."
	msgResellerSaved           = "Реселлер :code сохранён."
	msgResellerPayout          = "Выплата реселлеру :code записана."
	msgResellerDetached        = "Клуб :club отвязан от реселлера."
	msgResellerDeleted         = "Реселлер :code удалён."

	logResellerCreated  = "Новый реселлер: :name (код :code)"
	logResellerUpdated  = "Изменён реселлер :code"
	logResellerPayout   = "Выплата реселлеру :code: :amount ₸"
	logResellerDetached = "Клуб :club отвязан от реселлера :code"
	logResellerDeleted  = "Удалён реселлер :name (код :code)"

	maxResellerSearch = 120
)

// resellerHost — где входят реселлеры: `config('app.reseller_domain') ?? url('/reseller')`.
func (h *handlers) resellerHost(ctx context.Context) string {
	if h.d.ResellerDomain != "" {
		return h.d.ResellerDomain
	}
	if _, r := httpx.HTTPFrom(ctx); r != nil {
		return "https://" + r.Host + "/reseller" // панель работает только по HTTPS (cookie Secure)
	}
	return "/reseller"
}

// resellerError — ошибки сервиса реселлеров: нет реселлера (или клуб не его) — 404 (привязка модели маршрута,
// abort_unless), остальное — ошибки полей формы с текстами старого кода.
func resellerError(err error) error {
	var fe httpx.FormErrors
	if errors.Is(err, reseller.ErrPasswordRequired) {
		fe.AddErr(httpx.RuleError("password", cash.RuleRequired))
	}
	if errors.Is(err, reseller.ErrCodeTaken) {
		fe.AddErr(httpx.ValidationMsg("code", reseller.MsgCodeTaken, nil))
	}
	if e := fe.Err(); e != nil {
		return e
	}
	switch {
	case errors.Is(err, reseller.ErrNotFound):
		return httpx.NotFound()
	case errors.Is(err, reseller.ErrAlreadyReseller):
		return httpx.ValidationMsg("phone", reseller.MsgAlreadyReseller, nil)
	case errors.Is(err, reseller.ErrIdempotencyConflict):
		return httpx.NewAPIError(409, "idempotency_conflict", "")
	}
	return err
}

// SaListResellers — ResellerController::index(). Даты — по UTC (кабинет вне клуба).
func (h *handlers) SaListResellers(ctx context.Context, req superadminapi.SaListResellersRequestObject) (superadminapi.SaListResellersResponseObject, error) {
	if req.Params.Q != nil && utf8.RuneCountInString(*req.Params.Q) > maxResellerSearch {
		return nil, httpx.RuleErrorWith("q", "max_string", strconv.Itoa(maxResellerSearch))
	}
	// $like = '%'.addcslashes($search, '\\%_').'%'
	pattern := httpx.LikePattern(req.Params.Q)
	pg := page(req.Params.Page)
	out := superadminapi.ResellerBoard{Host: h.resellerHost(ctx), Items: []superadminapi.ResellerCard{}, Page: pg, PerPage: reseller.ListPage}
	err := h.tx(ctx, func(q *store.Queries) error {
		var err error
		if out.Total, err = q.ResellerCount(ctx, pattern); err != nil {
			return err
		}
		rows, err := q.ResellerList(ctx, store.ResellerListParams{Pattern: pattern, PageSize: reseller.ListPage, PageOffset: int64(pg-1) * reseller.ListPage})
		if err != nil {
			return err
		}
		ids := make([]int64, len(rows))
		for i, r := range rows {
			ids[i] = r.ID
		}
		clubs, err := q.ResellerClubsOf(ctx, ids)
		if err != nil {
			return err
		}
		byReseller := map[int64][]superadminapi.ResellerClub{}
		for _, c := range clubs {
			club := superadminapi.ResellerClub{Id: c.ID, Name: c.Name}
			if c.ResellerAttachedAt != nil {
				club.Attached = ptr(c.ResellerAttachedAt.UTC().Format("02.01.2006"))
			}
			byReseller[c.ResellerID] = append(byReseller[c.ResellerID], club)
		}
		for _, r := range rows {
			card := superadminapi.ResellerCard{Id: r.ID, Code: r.Code, Name: r.Name, UserName: r.UserName, UserPhone: r.UserPhone,
				CommissionBp: int(r.CommissionBp), DiscountPercent: int(r.DiscountPercent), IsActive: r.IsActive,
				EarnedTiyn: r.Earned, PaidOutTiyn: r.PaidOut, BalanceTiyn: reseller.Balance(r.Earned, r.PaidOut), Clubs: byReseller[r.ID]}
			if card.Clubs == nil {
				card.Clubs = []superadminapi.ResellerClub{}
			}
			out.Items = append(out.Items, card)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaListResellers200JSONResponse(out), nil
}

// resellerTerms — 'commission_percent' => numeric|min:0|max:100 (здесь — базисные пункты 0…10000),
// 'discount_percent' => integer|min:0|max:100.
func resellerTerms(fe *httpx.FormErrors, commissionBP, discountPercent int) {
	switch {
	case commissionBP < 0:
		fe.AddErr(httpx.RuleErrorWith("commission_percent", cash.RuleMin, "0"))
	case commissionBP > reseller.MaxCommissionBP:
		fe.AddErr(httpx.RuleErrorWith("commission_percent", cash.RuleMax, "100"))
	}
	switch {
	case discountPercent < 0:
		fe.AddErr(httpx.RuleErrorWith("discount_percent", cash.RuleMin, "0"))
	case discountPercent > reseller.MaxDiscountPercent:
		fe.AddErr(httpx.RuleErrorWith("discount_percent", cash.RuleMax, "100"))
	}
}

// SaCreateReseller — ResellerController::store(): новый аккаунт или существующий по телефону.
func (h *handlers) SaCreateReseller(ctx context.Context, req superadminapi.SaCreateResellerRequestObject) (superadminapi.SaCreateResellerResponseObject, error) {
	if h.d.Passwords == nil {
		return nil, httpx.ErrNotConfigured
	}
	b := req.Body
	if b == nil {
		b = &superadminapi.ResellerForm{}
	}
	// TrimStrings; пароль не обрезается (как в Laravel: поля password исключены).
	name, phone, code := strings.TrimSpace(b.Name), reseller.NormalizePhone(b.Phone), reseller.NormalizeCode(b.Code)
	password := ""
	if b.Password != nil {
		password = *b.Password
	}
	var fe httpx.FormErrors
	switch {
	case name == "":
		fe.AddErr(httpx.RuleError("name", cash.RuleRequired))
	case utf8.RuneCountInString(name) > reseller.MaxName:
		fe.AddErr(httpx.RuleErrorWith("name", "max_string", strconv.Itoa(reseller.MaxName)))
	}
	switch {
	case phone == "":
		fe.AddErr(httpx.RuleError("phone", cash.RuleRequired))
	case !reseller.ValidPhone(phone):
		fe.AddErr(httpx.ValidationMsg("phone", reseller.MsgPhoneInvalid, nil))
	}
	if password != "" && utf8.RuneCountInString(password) < reseller.MinPassword {
		fe.AddErr(httpx.RuleErrorWith("password", "min_string", strconv.Itoa(reseller.MinPassword)))
	}
	switch {
	case code == "":
		fe.AddErr(httpx.RuleError("code", cash.RuleRequired))
	case !reseller.ValidCode(code):
		fe.AddErr(httpx.ValidationMsg("code", reseller.MsgCodeFormat, nil))
	}
	resellerTerms(&fe, b.CommissionBp, b.DiscountPercent)
	if err := fe.Err(); err != nil {
		return nil, err
	}
	now := h.d.NowFunc()
	var created reseller.Created
	err := h.tx(ctx, func(q *store.Queries) error {
		var err error
		created, err = reseller.Create(ctx, q, reseller.CreateParams{Name: name, Phone: phone, Code: code, Password: password,
			CommissionBP: b.CommissionBp, DiscountPercent: b.DiscountPercent, Hash: h.d.Passwords.Hash, Now: now})
		if err != nil {
			return resellerError(err)
		}
		if err := h.log(ctx, q, activity.PlatformEntry{Event: "superadmin.reseller_created", Key: logResellerCreated,
			Params:      map[string]string{"name": created.Name, "code": created.Code},
			SubjectType: subjectReseller, SubjectID: created.ID}); err != nil {
			return err
		}
		return h.audit(ctx, q, 0, "permission", "reseller.created", subjectReseller, created.ID, nil, map[string]any{
			"code": created.Code, "user_id": created.UserID, "existing_account": created.ExistingAccount,
			"commission_bp": b.CommissionBp, "discount_percent": b.DiscountPercent})
	})
	if err != nil {
		return nil, err
	}
	key := msgResellerCreated
	if created.ExistingAccount {
		key = msgResellerCreatedExisting
	}
	return superadminapi.SaCreateReseller201JSONResponse(msg(h.d.T(ctx, key, map[string]string{"code": created.Code, "host": h.resellerHost(ctx)}))), nil
}

// phpBool — (string) true = "1", (string) false = "" (детали журнала старого кода: изменённые поля строками).
func phpBool(v bool) string {
	if v {
		return "1"
	}
	return ""
}

// SaUpdateReseller — ResellerController::update(): процент, скидка клубам и «код действует».
func (h *handlers) SaUpdateReseller(ctx context.Context, req superadminapi.SaUpdateResellerRequestObject) (superadminapi.SaUpdateResellerResponseObject, error) {
	b := req.Body
	if b == nil {
		b = &superadminapi.ResellerTermsForm{}
	}
	var fe httpx.FormErrors
	resellerTerms(&fe, b.CommissionBp, b.DiscountPercent)
	if err := fe.Err(); err != nil {
		return nil, err
	}
	now := h.d.NowFunc()
	var up reseller.Updated
	err := h.tx(ctx, func(q *store.Queries) error {
		var err error
		up, err = reseller.Update(ctx, q, req.ResellerId, reseller.Terms{CommissionBP: b.CommissionBp, DiscountPercent: b.DiscountPercent, Active: b.IsActive}, now)
		if err != nil {
			return resellerError(err)
		}
		if !up.Changed() {
			return nil
		}
		// collect($reseller->getChanges())->except('updated_at')->map(fn ($value) => (string) $value)
		changes := map[string]any{}
		if up.Before.CommissionBP != up.After.CommissionBP {
			changes["commission_percent"] = reseller.FormatPercent(up.After.CommissionBP)
		}
		if up.Before.DiscountPercent != up.After.DiscountPercent {
			changes["discount_percent"] = strconv.Itoa(up.After.DiscountPercent)
		}
		if up.Before.Active != up.After.Active {
			changes["is_active"] = phpBool(up.After.Active)
		}
		if err := h.log(ctx, q, activity.PlatformEntry{Event: "superadmin.reseller_updated", Key: logResellerUpdated,
			Params: map[string]string{"code": up.Code}, SubjectType: subjectReseller, SubjectID: up.ID, Details: changes}); err != nil {
			return err
		}
		return h.audit(ctx, q, 0, "tariff", "reseller.updated", subjectReseller, up.ID, termsAudit(up.Before), termsAudit(up.After))
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaUpdateReseller200JSONResponse(msg(h.d.T(ctx, msgResellerSaved, map[string]string{"code": up.Code}))), nil
}

func termsAudit(t reseller.Terms) map[string]any {
	return map[string]any{"commission_bp": t.CommissionBP, "discount_percent": t.DiscountPercent, "is_active": t.Active}
}

// SaPayoutReseller — ResellerController::payout() + ключ идемпотентности и свежий код TOTP.
func (h *handlers) SaPayoutReseller(ctx context.Context, req superadminapi.SaPayoutResellerRequestObject) (superadminapi.SaPayoutResellerResponseObject, error) {
	if ro := readOnly(ctx); ro != nil {
		return superadminapi.SaPayoutReseller403JSONResponse{ForbiddenJSONResponse: *ro}, nil
	}
	b := req.Body
	if b == nil {
		b = &superadminapi.ResellerPayoutForm{}
	}
	note := ""
	if b.Note != nil {
		note = strings.TrimSpace(*b.Note)
	}
	// 'amount' => numeric|min:0.01|max:999999999; 'note' => nullable|string|max:255.
	var fe httpx.FormErrors
	switch {
	case b.AmountTiyn < reseller.MinPayout:
		fe.AddErr(httpx.RuleErrorWith("amount", cash.RuleMin, "0.01"))
	case b.AmountTiyn > reseller.MaxPayout:
		fe.AddErr(httpx.RuleErrorWith("amount", cash.RuleMax, "999999999"))
	}
	if utf8.RuneCountInString(note) > reseller.MaxNote {
		fe.AddErr(httpx.RuleErrorWith("note", "max_string", strconv.Itoa(reseller.MaxNote)))
	}
	if err := fe.Err(); err != nil {
		return nil, err
	}
	if limited, err := h.confirmSecondFactor(ctx, &superadminapi.SecondFactorConfirm{Code: b.Code}); err != nil {
		return nil, err
	} else if limited != nil {
		return superadminapi.SaPayoutReseller429JSONResponse{TooManyRequestsJSONResponse: *limited}, nil
	}
	now := h.d.NowFunc()
	by := httpx.Principal(ctx).UserID
	var paid reseller.PaidOut
	err := h.tx(ctx, func(q *store.Queries) error {
		var err error
		// Ключ формы — в пространстве суперадмина: чужой ключ не совпадёт со своим.
		paid, err = reseller.Payout(ctx, q, reseller.PayoutParams{ResellerID: req.ResellerId, Amount: b.AmountTiyn, Note: note, By: by,
			Key: "superadmin:" + strconv.FormatInt(by, 10) + ":" + req.Params.IdempotencyKey, Now: now})
		if err != nil {
			return resellerError(err)
		}
		if paid.Replayed {
			return nil // выплата уже записана первым запросом вместе с журналами
		}
		// Сумма в тексте — как $payout->amount (decimal:2): «300.00»; в деталях — число тенге, как (float) в старом.
		if err := h.log(ctx, q, activity.PlatformEntry{Event: "superadmin.reseller_payout", Key: logResellerPayout,
			Params:      map[string]string{"code": paid.Code, "amount": strconv.FormatInt(paid.Amount/100, 10) + "." + twoDigits(paid.Amount%100)},
			SubjectType: subjectReseller, SubjectID: req.ResellerId,
			Details: map[string]any{"amount": json.Number(reseller.FormatTenge(paid.Amount))}}); err != nil {
			return err
		}
		after := map[string]any{"reseller_id": req.ResellerId, "code": paid.Code, "amount": paid.Amount}
		if note != "" {
			after["note"] = note
		}
		return h.audit(ctx, q, 0, "money", "reseller.payout", subjectPayout, paid.ID, nil, after)
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaPayoutReseller200JSONResponse(msg(h.d.T(ctx, msgResellerPayout, map[string]string{"code": paid.Code}))), nil
}

// SaDetachResellerClub — ResellerController::detach(): клуб больше не клуб реселлера.
func (h *handlers) SaDetachResellerClub(ctx context.Context, req superadminapi.SaDetachResellerClubRequestObject) (superadminapi.SaDetachResellerClubResponseObject, error) {
	now := h.d.NowFunc()
	var d reseller.Detached
	err := h.tx(ctx, func(q *store.Queries) error {
		var err error
		if d, err = reseller.Detach(ctx, q, req.ResellerId, req.ClubId, now); err != nil {
			return resellerError(err)
		}
		if err := h.log(ctx, q, activity.PlatformEntry{Event: "superadmin.reseller_detached", Key: logResellerDetached,
			Params: map[string]string{"club": d.ClubName, "code": d.Code}, ClubID: d.ClubID,
			SubjectType: subjectReseller, SubjectID: d.ResellerID}); err != nil {
			return err
		}
		return h.audit(ctx, q, d.ClubID, "tariff", "reseller.detached", subjectReseller, d.ResellerID,
			map[string]any{"reseller_id": d.ResellerID, "code": d.Code}, map[string]any{"reseller_id": nil})
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaDetachResellerClub200JSONResponse(msg(h.d.T(ctx, msgResellerDetached, map[string]string{"club": d.ClubName}))), nil
}

// SaDeleteReseller — ResellerController::destroy() + свежий код TOTP: аккаунт остаётся, клубы отвязываются, выплаты
// удаляются (их суммы остаются в журнале денег платформы).
func (h *handlers) SaDeleteReseller(ctx context.Context, req superadminapi.SaDeleteResellerRequestObject) (superadminapi.SaDeleteResellerResponseObject, error) {
	if ro := readOnly(ctx); ro != nil {
		return superadminapi.SaDeleteReseller403JSONResponse{ForbiddenJSONResponse: *ro}, nil
	}
	if limited, err := h.confirmSecondFactor(ctx, req.Body); err != nil {
		return nil, err
	} else if limited != nil {
		return superadminapi.SaDeleteReseller429JSONResponse{TooManyRequestsJSONResponse: *limited}, nil
	}
	var del reseller.Deleted
	err := h.tx(ctx, func(q *store.Queries) error {
		var err error
		if del, err = reseller.Delete(ctx, q, req.ResellerId); err != nil {
			return resellerError(err)
		}
		// В старом — без предмета (реселлера уже нет), детали — код, аккаунт, число клубов.
		details := map[string]any{"code": del.Code, "user_id": del.UserID, "clubs": del.Clubs}
		if err := h.log(ctx, q, activity.PlatformEntry{Event: "superadmin.reseller_deleted", Key: logResellerDeleted,
			Params: map[string]string{"name": del.Name, "code": del.Code}, Details: details}); err != nil {
			return err
		}
		return h.audit(ctx, q, 0, "permission", "reseller.deleted", subjectReseller, del.ID, details, nil)
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaDeleteReseller200JSONResponse(msg(h.d.T(ctx, msgResellerDeleted, map[string]string{"code": del.Code}))), nil
}

func twoDigits(n int64) string {
	if n < 10 {
		return "0" + strconv.FormatInt(n, 10)
	}
	return strconv.FormatInt(n, 10)
}
