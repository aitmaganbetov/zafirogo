package superadmin

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/mfa"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	apicommon "github.com/aitmaganbetov/zafirogo/server/internal/http/api/common"
	superadminapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/superadmin"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Laravel: app/Http/Controllers/Superadmin/UserController.php.
const (
	accountsPage = 30
	subjectUser  = `App\Models\User`
)

var nonDigitsRe = regexp.MustCompile(`\D+`)

// SaListAccounts — UserController::index(): аккаунты по имени, поиск по имени или цифрам телефона.
func (h *handlers) SaListAccounts(ctx context.Context, req superadminapi.SaListAccountsRequestObject) (superadminapi.SaListAccountsResponseObject, error) {
	search := ""
	if req.Params.Search != nil {
		if len([]rune(*req.Params.Search)) > 100 {
			return nil, httpx.RuleErrorWith("search", "max_string", "100")
		}
		search = strings.TrimSpace(*req.Params.Search)
	}
	filter := ""
	if req.Params.Filter != nil {
		filter = string(*req.Params.Filter)
		if filter != "blocked" && filter != "superadmin" {
			return nil, httpx.RuleError("filter", "in")
		}
	}
	digits := nonDigitsRe.ReplaceAllString(search, "")
	pg := page(req.Params.Page)
	self := httpx.Principal(ctx).UserID
	out := superadminapi.AccountList{Items: []superadminapi.Account{}, Page: pg, PerPage: accountsPage}
	err := h.tx(ctx, func(q *store.Queries) error {
		var err error
		if out.Total, err = q.SuperadminCountUsers(ctx, store.SuperadminCountUsersParams{Search: search, Digits: digits, Filter: filter}); err != nil {
			return err
		}
		users, err := q.SuperadminUsers(ctx, store.SuperadminUsersParams{Search: search, Digits: digits, Filter: filter,
			PageSize: accountsPage, PageOffset: int64(pg-1) * accountsPage})
		if err != nil {
			return err
		}
		ids := make([]int64, len(users))
		for i, u := range users {
			ids[i] = u.ID
		}
		ms, err := q.SuperadminUserMemberships(ctx, ids)
		if err != nil {
			return err
		}
		byUser := map[int64][]superadminapi.AccountMembership{}
		for _, m := range ms {
			byUser[m.UserID] = append(byUser[m.UserID], superadminapi.AccountMembership{ClubId: m.ClubID, ClubName: m.ClubName, Role: m.Role})
		}
		for _, u := range users {
			a := superadminapi.Account{Id: u.ID, Name: u.Name, Phone: u.Phone, IsSuperadmin: u.IsSuperadmin, Self: u.ID == self,
				SecondFactor: u.SecondFactor, Memberships: byUser[u.ID]}
			if a.Memberships == nil {
				a.Memberships = []superadminapi.AccountMembership{}
			}
			if u.BlockedAt != nil {
				a.BlockedSince = ptr(u.BlockedAt.UTC().Format("02.01.2006"))
			}
			out.Items = append(out.Items, a)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaListAccounts200JSONResponse(out), nil
}

// lockUser — пользователь под блокировкой строки (404, если нет).
func lockUser(ctx context.Context, q *store.Queries, id int64) (store.SuperadminLockUserRow, error) {
	u, err := q.SuperadminLockUser(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, httpx.NotFound()
	}
	return u, err
}

// guardOther — своё действие к своему аккаунту нельзя (UserController::guardOther()).
func guardOther(ctx context.Context, id int64) error {
	if httpx.Principal(ctx).UserID == id {
		return httpx.ValidationMsg("user", "Это действие нельзя применить к своему аккаунту.", nil)
	}
	return nil
}

// readOnly — режим просмотра клуба: опасные действия кабинета закрыты (readOnlyGuard кабинет пропускает).
func readOnly(ctx context.Context) *apicommon.ForbiddenJSONResponse {
	if !httpx.Principal(ctx).ReadOnly {
		return nil
	}
	r := apicommon.ForbiddenJSONResponse(httpx.ErrorBody(permissions.CodeReadOnly, permissions.MsgReadOnly))
	return &r
}

// Тексты проверки свежего кода — те же, что на экране второго фактора при входе (sections/auth).
const (
	msgCodeRequired = "Введите код."
	msgCodeInvalid  = "Неверный код."
)

// confirmSecondFactor — свежее подтверждение вторым фактором действующего суперадмина (аудит A-10): выдача и снятие
// прав суперадмина и сброс чужого второго фактора выполняются только с кодом TOTP из тела запроса. Украденной
// сессии (токена) для этих действий недостаточно. Проверка — mfa.Confirm: лимит superadmin-totp, защита от повтора
// шага, неудача — в журнал платформы. limited != nil — ответ 429; err — 422 по полю code.
func (h *handlers) confirmSecondFactor(ctx context.Context, body *superadminapi.SecondFactorConfirm) (limited *apicommon.TooManyRequestsJSONResponse, err error) {
	if h.d.Auth == nil {
		return nil, httpx.ErrNotConfigured
	}
	if body == nil || strings.TrimSpace(body.Code) == "" {
		return nil, httpx.ValidationMsg("code", msgCodeRequired, nil)
	}
	err = h.d.Auth.ConfirmSecondFactor(ctx, httpx.Principal(ctx).UserID, body.Code)
	var lim *mfa.LimitError
	switch {
	case err == nil:
		return nil, nil
	case errors.As(err, &lim):
		sec := int(ratelimit.RetryAfterSeconds(lim.RetryAfter))
		return &apicommon.TooManyRequestsJSONResponse{
			Body:    httpx.ErrorBody("too_many_requests", ratelimit.TooManyAttemptsMessage),
			Headers: apicommon.TooManyRequestsResponseHeaders{RetryAfter: &sec},
		}, nil
	case errors.Is(err, mfa.ErrInvalidCode), errors.Is(err, mfa.ErrNotConfigured):
		return nil, httpx.ValidationMsg("code", msgCodeInvalid, nil)
	case errors.Is(err, mfa.ErrSecretUnreadable):
		h.d.Log().ErrorContext(ctx, "superadmin totp secret cannot be decrypted with any data key")
		return nil, httpx.ValidationMsg("code", msgCodeInvalid, nil)
	case errors.Is(err, auth.ErrSecondFactorUnavailable):
		return nil, httpx.ErrNotConfigured
	}
	return nil, err
}

// SaBlockAccount — UserController::block(): вход закрыт, сеансы панели и токены гостя на ПК отзываются сразу.
func (h *handlers) SaBlockAccount(ctx context.Context, req superadminapi.SaBlockAccountRequestObject) (superadminapi.SaBlockAccountResponseObject, error) {
	if err := guardOther(ctx, req.UserId); err != nil {
		return nil, err
	}
	now := h.d.NowFunc()
	var clubs []int64
	err := h.tx(ctx, func(q *store.Queries) error {
		u, err := lockUser(ctx, q, req.UserId)
		if err != nil {
			return err
		}
		if u.IsSuperadmin {
			return httpx.ValidationMsg("user", "Сначала снимите с пользователя права суперадмина.", nil)
		}
		if err := q.SuperadminSetUserBlocked(ctx, store.SuperadminSetUserBlockedParams{BlockedAt: &now, Now: &now, ID: u.ID}); err != nil {
			return err
		}
		ms, err := q.SuperadminUserMemberships(ctx, []int64{u.ID})
		if err != nil {
			return err
		}
		for _, m := range ms {
			clubs = append(clubs, m.ClubID)
		}
		if err := h.log(ctx, q, activity.PlatformEntry{Event: "superadmin.user_blocked", Key: "Пользователь :user заблокирован",
			Params: map[string]string{"user": u.Name}, SubjectType: subjectUser, SubjectID: u.ID}); err != nil {
			return err
		}
		return h.audit(ctx, q, 0, "security", "user.blocked", subjectUser, u.ID, map[string]any{"blocked": false}, map[string]any{"blocked": true})
	})
	if err != nil {
		return nil, err
	}
	// После коммита: сеансы панели и токены гостя на ПК (в старом — sessions и Sanctum-токены в той же транзакции).
	if h.d.Auth != nil {
		if err := h.d.Auth.Sessions().RevokeUser(ctx, req.UserId); err != nil {
			h.d.Log().WarnContext(ctx, "superadmin: revoke sessions", "user_id", req.UserId, "err", err)
		}
	}
	if h.d.SignOutGuest != nil && len(clubs) > 0 {
		h.d.SignOutGuest(ctx, clubs[0], req.UserId, "")
	}
	return superadminapi.SaBlockAccount200JSONResponse(msg(h.d.T(ctx, "Пользователь заблокирован.", nil))), nil
}

// SaUnblockAccount — UserController::unblock().
func (h *handlers) SaUnblockAccount(ctx context.Context, req superadminapi.SaUnblockAccountRequestObject) (superadminapi.SaUnblockAccountResponseObject, error) {
	now := h.d.NowFunc()
	err := h.tx(ctx, func(q *store.Queries) error {
		u, err := lockUser(ctx, q, req.UserId)
		if err != nil {
			return err
		}
		if err := q.SuperadminSetUserBlocked(ctx, store.SuperadminSetUserBlockedParams{Now: &now, ID: u.ID}); err != nil {
			return err
		}
		if err := h.log(ctx, q, activity.PlatformEntry{Event: "superadmin.user_unblocked", Key: "Пользователь :user разблокирован",
			Params: map[string]string{"user": u.Name}, SubjectType: subjectUser, SubjectID: u.ID}); err != nil {
			return err
		}
		return h.audit(ctx, q, 0, "security", "user.unblocked", subjectUser, u.ID, map[string]any{"blocked": u.BlockedAt != nil}, map[string]any{"blocked": false})
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaUnblockAccount200JSONResponse(msg(h.d.T(ctx, "Пользователь разблокирован.", nil))), nil
}

// SaGrantSuperadmin — UserController::grant(). Отличие от Laravel (аудит A-10, подтверждено владельцем): нужен
// свежий код TOTP действующего суперадмина; в режиме просмотра клуба — нельзя.
func (h *handlers) SaGrantSuperadmin(ctx context.Context, req superadminapi.SaGrantSuperadminRequestObject) (superadminapi.SaGrantSuperadminResponseObject, error) {
	if ro := readOnly(ctx); ro != nil {
		return superadminapi.SaGrantSuperadmin403JSONResponse{ForbiddenJSONResponse: *ro}, nil
	}
	if limited, err := h.confirmSecondFactor(ctx, req.Body); err != nil {
		return nil, err
	} else if limited != nil {
		return superadminapi.SaGrantSuperadmin429JSONResponse{TooManyRequestsJSONResponse: *limited}, nil
	}
	now := h.d.NowFunc()
	err := h.tx(ctx, func(q *store.Queries) error {
		u, err := lockUser(ctx, q, req.UserId)
		if err != nil {
			return err
		}
		if u.BlockedAt != nil {
			return httpx.ValidationMsg("user", "Заблокированному пользователю нельзя выдать права суперадмина.", nil)
		}
		if err := q.SuperadminSetUserSuperadmin(ctx, store.SuperadminSetUserSuperadminParams{IsSuperadmin: true, Now: &now, ID: u.ID}); err != nil {
			return err
		}
		if err := h.log(ctx, q, activity.PlatformEntry{Event: "superadmin.granted", Key: "Пользователю :user выданы права суперадмина",
			Params: map[string]string{"user": u.Name}, SubjectType: subjectUser, SubjectID: u.ID}); err != nil {
			return err
		}
		return h.audit(ctx, q, 0, "permission", "superadmin.granted", subjectUser, u.ID, map[string]any{"is_superadmin": u.IsSuperadmin}, map[string]any{"is_superadmin": true})
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaGrantSuperadmin200JSONResponse(msg(h.d.T(ctx, "Права суперадмина выданы.", nil))), nil
}

// SaRevokeSuperadmin — UserController::revoke() + свежий код TOTP (аудит A-10), не в режиме просмотра. Флаг суперадмина читается при каждом запросе (Principal), поэтому
// права пропадают со следующего запроса.
func (h *handlers) SaRevokeSuperadmin(ctx context.Context, req superadminapi.SaRevokeSuperadminRequestObject) (superadminapi.SaRevokeSuperadminResponseObject, error) {
	if ro := readOnly(ctx); ro != nil {
		return superadminapi.SaRevokeSuperadmin403JSONResponse{ForbiddenJSONResponse: *ro}, nil
	}
	if err := guardOther(ctx, req.UserId); err != nil {
		return nil, err
	}
	if limited, err := h.confirmSecondFactor(ctx, req.Body); err != nil {
		return nil, err
	} else if limited != nil {
		return superadminapi.SaRevokeSuperadmin429JSONResponse{TooManyRequestsJSONResponse: *limited}, nil
	}
	now := h.d.NowFunc()
	err := h.tx(ctx, func(q *store.Queries) error {
		u, err := lockUser(ctx, q, req.UserId)
		if err != nil {
			return err
		}
		if err := q.SuperadminSetUserSuperadmin(ctx, store.SuperadminSetUserSuperadminParams{IsSuperadmin: false, Now: &now, ID: u.ID}); err != nil {
			return err
		}
		if err := h.log(ctx, q, activity.PlatformEntry{Event: "superadmin.revoked", Key: "У пользователя :user сняты права суперадмина",
			Params: map[string]string{"user": u.Name}, SubjectType: subjectUser, SubjectID: u.ID}); err != nil {
			return err
		}
		return h.audit(ctx, q, 0, "permission", "superadmin.revoked", subjectUser, u.ID, map[string]any{"is_superadmin": u.IsSuperadmin}, map[string]any{"is_superadmin": false})
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaRevokeSuperadmin200JSONResponse(msg(h.d.T(ctx, "Права суперадмина сняты.", nil))), nil
}

// SaResetSecondFactor — сброс второго фактора суперадмина другим суперадмином (Этап 7, новая функция;
// docs/open-questions/stage7-totp.md): ключ TOTP и коды восстановления удаляются, сеансы отзываются; следующий вход —
// принудительная настройка. Не в режиме просмотра клуба (readOnlyGuard кабинет пропускает — запрет здесь и в
// readOnlyAllowed) и не для своего аккаунта; со свежим кодом TOTP действующего суперадмина (аудит A-10).
func (h *handlers) SaResetSecondFactor(ctx context.Context, req superadminapi.SaResetSecondFactorRequestObject) (superadminapi.SaResetSecondFactorResponseObject, error) {
	if ro := readOnly(ctx); ro != nil {
		return superadminapi.SaResetSecondFactor403JSONResponse{ForbiddenJSONResponse: *ro}, nil
	}
	if err := guardOther(ctx, req.UserId); err != nil {
		return nil, err
	}
	if limited, err := h.confirmSecondFactor(ctx, req.Body); err != nil {
		return nil, err
	} else if limited != nil {
		return superadminapi.SaResetSecondFactor429JSONResponse{TooManyRequestsJSONResponse: *limited}, nil
	}
	err := h.tx(ctx, func(q *store.Queries) error {
		u, err := lockUser(ctx, q, req.UserId)
		if err != nil {
			return err
		}
		had, err := mfa.Reset(ctx, q, u.ID)
		if err != nil {
			return err
		}
		if !had {
			return httpx.ValidationMsg("user", "У пользователя не настроена двухфакторная аутентификация.", nil)
		}
		if err := h.log(ctx, q, activity.PlatformEntry{Event: mfa.EventReset, Key: mfa.MsgReset,
			Params: map[string]string{"user": u.Name}, SubjectType: subjectUser, SubjectID: u.ID}); err != nil {
			return err
		}
		return h.audit(ctx, q, 0, "security", "superadmin.totp_reset", subjectUser, u.ID, map[string]any{"second_factor": true}, map[string]any{"second_factor": false})
	})
	if err != nil {
		return nil, err
	}
	// После коммита: сеансы пользователя (вошедшие с прежним ключом) — заново, с настройкой нового.
	if h.d.Auth != nil {
		if err := h.d.Auth.Sessions().RevokeUser(ctx, req.UserId); err != nil {
			h.d.Log().WarnContext(ctx, "superadmin: revoke sessions", "user_id", req.UserId, "err", err)
		}
	}
	return superadminapi.SaResetSecondFactor200JSONResponse(msg(h.d.T(ctx, "Двухфакторная аутентификация сброшена.", nil))), nil
}
