package guestapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/identity"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/bonus"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/pricing"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Сообщения покупок (AgentTariffPurchaseService::purchase()/purchaseMinutes()/startPackage(), последний аргумент sell()).
const (
	msgTariffBought    = "Тариф подключён."
	msgMinutesBought   = "Время добавлено."
	msgPackageStarted  = "Абонемент запущен."
	msgMinutesRange    = "Можно купить от 1 до 720 минут."
	msgNameMin         = "Имя — не короче 2 символов."                                   // GuestProfileController::name()
	msgCurrentWrong    = "Текущий пароль неверный."                                      // GuestProfileController::password()
	msgPasswordsMin    = "Пароль — не короче 6 символов."                                // #nosec G101 -- текст для пользователя, не секрет
	msgPasswordSignOut = "Пароль изменён на другом устройстве. Войдите с новым паролем." // #nosec G101 -- текст для пользователя, не секрет
)

type guestService struct {
	agentv1.UnimplementedGuestServiceServer
	s *Server
}

var _ agentv1.GuestServiceServer = (*guestService)(nil)

// actor — кто действует (ledger, аудит): гость с ПК.
func actor(ctx context.Context, g Guest) billing.BalanceActor {
	a := billing.BalanceActor{UserID: g.UserID, Kind: "guest"}
	if ip := peerIP(ctx); ip != "" {
		if addr, err := parseAddr(ip); err == nil {
			a.IP = addr
		}
	}
	return a
}

// GetProfile — профиль и балансы (GET /api/me, AuthController::summary()) + счётчики гостя.
func (gs *guestService) GetProfile(ctx context.Context, _ *agentv1.GetProfileRequest) (*agentv1.GetProfileResponse, error) {
	s := gs.s
	ctx, id, g, err := s.guestCall(ctx, "")
	if err != nil {
		return nil, err
	}
	out := &agentv1.GetProfileResponse{}
	err = db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		var err error
		if out.User, out.Balance, _, err = s.profile(ctx, q, id, g.UserID, s.o.Now()); err != nil {
			return err
		}
		pending, err := q.GuestPendingFriendRequests(ctx, store.GuestPendingFriendRequestsParams{ClubID: id.ClubID, UserID: g.UserID})
		if err != nil {
			return err
		}
		out.PendingFriendRequests = uint32(min(max(pending, 0), 1<<31)) // #nosec G115 -- ограничено
		out.LastStaffChatMessageId, err = q.GuestLastStaffChatMessageID(ctx, store.GuestLastStaffChatMessageIDParams{ClubID: id.ClubID, UserID: g.UserID})
		return err
	})
	if err != nil {
		return nil, s.fail(ctx, "GetProfile", err)
	}
	return out, nil
}

// UpdateName — смена имени (POST /api/guest/profile/name, GuestProfileController::name()).
func (gs *guestService) UpdateName(ctx context.Context, req *agentv1.UpdateNameRequest) (*agentv1.UpdateNameResponse, error) {
	s := gs.s
	ctx, id, g, err := s.guestCall(ctx, "api.guest.profile.name")
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	if n := utf8.RuneCountInString(req.GetName()); n < 2 || n > 255 || utf8.RuneCountInString(name) < 2 {
		return nil, validation("name", msgNameMin)
	}
	now := s.o.Now()
	err = db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		u, err := q.GuestUserInClub(ctx, store.GuestUserInClubParams{ClubID: id.ClubID, UserID: g.UserID})
		if err != nil {
			return err
		}
		if u.Name == name {
			return nil
		}
		if _, err := q.GuestUpdateUserName(ctx, store.GuestUpdateUserNameParams{Name: name, Now: &now, UserID: g.UserID, ClubID: id.ClubID}); err != nil {
			return err
		}
		return s.record(ctx, q, activity.Entry{ClubID: id.ClubID, Event: "user.profile_updated",
			Description: "Клиент изменил имя в агенте: " + name, UserID: g.UserID, SubjectType: subjectUser, SubjectID: g.UserID,
			Details: map[string]any{"name": u.Name + " → " + name}})
	})
	if err != nil {
		return nil, s.fail(ctx, "UpdateName", err)
	}
	return &agentv1.UpdateNameResponse{Name: name}, nil
}

// ChangePassword — смена пароля с текущим (POST /api/guest/profile/password, GuestProfileController::password()):
// остальные токены гостя отзываются (их ПК получают SignOutGuest), этот остаётся.
func (gs *guestService) ChangePassword(ctx context.Context, req *agentv1.ChangePasswordRequest) (*agentv1.ChangePasswordResponse, error) {
	s := gs.s
	ctx, id, g, err := s.guestCall(ctx, "api.guest.profile.password")
	if err != nil {
		return nil, err
	}
	if req.GetCurrentPassword() == "" || len(req.GetCurrentPassword()) > 256 {
		return nil, validation("current_password", "Введите текущий пароль.")
	}
	if n := utf8.RuneCountInString(req.GetNewPassword()); n < 6 || n > 255 {
		return nil, validation("password", msgPasswordsMin)
	}
	var user store.GuestUserInClubRow
	err = db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		var err error
		user, err = q.GuestUserInClub(ctx, store.GuestUserInClubParams{ClubID: id.ClubID, UserID: g.UserID})
		return err
	})
	if err != nil {
		return nil, s.fail(ctx, "ChangePassword", err)
	}
	if _, err := s.o.Passwords.Verify(ctx, req.GetCurrentPassword(), user.Password); err != nil {
		return nil, rpcError(codes.PermissionDenied, agentv1.ErrorCode_ERROR_CODE_WRONG_PASSWORD, msgCurrentWrong, nil, "current_password")
	}
	hash, err := s.o.Passwords.Hash(ctx, req.GetNewPassword())
	if err != nil {
		return nil, s.fail(ctx, "ChangePassword", err)
	}
	now := s.o.Now()
	err = db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		if _, err := q.GuestUpdateUserPassword(ctx, store.GuestUpdateUserPasswordParams{PasswordHash: hash, Now: &now, UserID: g.UserID, ClubID: id.ClubID}); err != nil {
			return err
		}
		return s.record(ctx, q, activity.Entry{ClubID: id.ClubID, Event: "user.password_changed",
			Description: "Клиент сменил пароль в агенте: " + user.Name, UserID: g.UserID, SubjectType: subjectUser, SubjectID: g.UserID})
	})
	if err != nil {
		return nil, s.fail(ctx, "ChangePassword", err)
	}
	pcs, err := s.tokens.RevokeUser(ctx, g.UserID, g)
	if err != nil {
		s.o.Log.WarnContext(ctx, "guest tokens not revoked after password change", "user_id", g.UserID, "err", err)
	}
	s.signOutOn(ctx, id.ClubID, g.UserID, pcs, msgPasswordSignOut)
	return &agentv1.ChangePasswordResponse{}, nil
}

// signOutOn — SignOutGuest на ПК, где у гостя отозваны токены (только ПК этого клуба: токены клубные).
func (s *Server) signOutOn(ctx context.Context, clubID, userID int64, pcs []int64, reason string) {
	if s.o.Commands == nil {
		return
	}
	for _, pcID := range pcs {
		if _, err := s.o.Commands.Send(ctx, clubID, pcID, &agentv1.Command{
			Kind: &agentv1.Command_SignOutGuest{SignOutGuest: &agentv1.SignOutGuest{UserId: userID, Reason: reason}},
		}); err != nil {
			s.o.Log.WarnContext(ctx, "sign out guest command not sent", "computer_id", pcID, "err", err)
		}
	}
}

// ---------------------------------------------------------------------------
// Тарифы и минуты
// ---------------------------------------------------------------------------

// ListTariffs — тарифы, которые продаются на этом ПК (GET /api/tariffs, AgentTariffPurchaseService::availableFor()
// с зоной ПК, representation()).
func (gs *guestService) ListTariffs(ctx context.Context, _ *agentv1.ListTariffsRequest) (*agentv1.ListTariffsResponse, error) {
	s := gs.s
	ctx, id, g, err := s.guestCall(ctx, "api.tariffs.index")
	if err != nil {
		return nil, err
	}
	out := &agentv1.ListTariffsResponse{}
	err = db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		out.Tariffs = nil
		now := s.o.Now()
		ci, err := loadClub(ctx, q, id)
		if err != nil {
			return err
		}
		u, err := q.GuestUserInClub(ctx, store.GuestUserInClubParams{ClubID: id.ClubID, UserID: g.UserID})
		if err != nil {
			return err // $user->currentMembership()->firstOrFail()
		}
		pcRow, err := q.GuestComputer(ctx, store.GuestComputerParams{ClubID: id.ClubID, ID: id.ComputerID})
		if err != nil {
			return err
		}
		rows, err := q.GuestListTariffs(ctx, id.ClubID)
		if err != nil {
			return err
		}
		discount := bonus.LoyaltyDiscountFor(ci.settings.bonus.LoyaltyLevelsInForce(), u.Discount, money.Tiyn(u.TotalSpent))
		zone := pcRow.ClubZoneID
		for _, r := range rows {
			t, err := tariffFromRow(r)
			if err != nil {
				s.o.Log.WarnContext(ctx, "tariff skipped: bad periods", "tariff_id", r.ID, "err", err)
				continue
			}
			if !t.InZone(&zone) || t.Availability(u.Role, r.Sold, now, ci.loc) != pricing.Available {
				continue
			}
			offer, err := t.Offer(ci.settings.rules, discount)
			if err != nil {
				return err
			}
			pt := &agentv1.Tariff{
				TariffId: r.ID, Name: r.Name, Minutes: int32(min(offer.Minutes, 1<<31-1)), // #nosec G115 -- ограничено
				PriceTiyn: int64(offer.Price), OriginalPriceTiyn: int64(offer.OriginalPrice), Currency: r.Currency,
				BonusMaxPercent: uint32(max(offer.BonusMaxPercent, 0)), // #nosec G115 -- 0…100
				MixedPayment:    offer.MixedPayment, ZoneIds: r.ZoneIds,
			}
			if r.Description != nil {
				pt.Description = *r.Description
			}
			if offer.UsageLimitMinutes != nil {
				pt.UsageLimitMinutes = int32(min(*offer.UsageLimitMinutes, 1<<31-1)) // #nosec G115 -- ограничено
			}
			out.Tariffs = append(out.Tariffs, pt)
		}
		return nil
	})
	if err != nil {
		return nil, s.fail(ctx, "ListTariffs", err)
	}
	return out, nil
}

func tariffFromRow(r store.GuestListTariffsRow) (pricing.Tariff, error) {
	sale, err := pricing.ParseWeeklyPeriods(r.SalePeriods)
	if err != nil {
		return pricing.Tariff{}, err
	}
	validity, err := pricing.ParseWeeklyPeriods(r.ValidityPeriods)
	if err != nil {
		return pricing.Tariff{}, err
	}
	return pricing.Tariff{
		Price: money.Tiyn(r.Price), DurationMinutes: r.DurationMinutes, PaymentMethod: pricing.PaymentMethod(r.PaymentMethod),
		DiscountEnabled: r.DiscountEnabled, IsActive: r.IsActive, UsageLimitEnabled: r.UsageLimitEnabled,
		UsageLimitMinutes: r.UsageLimitMinutes, QuantityLimitEnabled: r.QuantityLimitEnabled, QuantityLimit: r.QuantityLimit,
		UserRoles: userRoles(r.UserRoles), SalePeriods: sale, ValidityPeriods: validity, ZoneIDs: r.ZoneIds,
	}, nil
}

// nonStringRole — элемент user_roles не строка: in_array($role, $roles, true) его не находит, но массив не пуст.
const nonStringRole = "\x00non-string"

// userRoles — tariffs.user_roles (каст 'array'); null/не массив — без ограничения
// (AgentTariffPurchaseService::isAvailable() стр. 323–326; как billing/service.userRoles).
func userRoles(raw json.RawMessage) []string {
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) != nil {
			return nil
		}
		for _, v := range obj {
			list = append(list, v)
		}
	}
	if list == nil {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		var s string
		if json.Unmarshal(v, &s) == nil {
			out = append(out, s)
		} else {
			out = append(out, nonStringRole)
		}
	}
	return out
}

func balanceChoice(b agentv1.BalanceSource) pricing.BalanceChoice {
	switch b {
	case agentv1.BalanceSource_BALANCE_SOURCE_MAIN:
		return pricing.ChoiceMain
	case agentv1.BalanceSource_BALANCE_SOURCE_BONUS:
		return pricing.ChoiceBonus
	}
	return pricing.ChoiceNone
}

// PurchaseTariff — покупка тарифа на этом ПК (POST /api/tariffs/{tariff}/purchase, AgentTariffPurchaseService::purchase()).
func (gs *guestService) PurchaseTariff(ctx context.Context, req *agentv1.PurchaseTariffRequest) (*agentv1.PurchaseTariffResponse, error) {
	s := gs.s
	ctx, id, g, err := s.guestCall(ctx, "api.tariffs.purchase")
	if err != nil {
		return nil, err
	}
	return once(ctx, s.idem, id.ComputerID, "PurchaseTariff", req.GetIdempotencyKey(), req, &agentv1.PurchaseTariffResponse{},
		func() (*agentv1.PurchaseTariffResponse, error) {
			if req.GetTariffId() <= 0 {
				return nil, validation("tariff_id", "Тариф недоступен.")
			}
			res, err := s.o.Billing.PurchaseTariff(ctx, billingsvc.TariffPurchaseRequest{
				ClubID: id.ClubID, UserID: g.UserID, TariffID: req.GetTariffId(), ComputerID: id.ComputerID,
				Balance: balanceChoice(req.GetBalanceSource()), IdempotencyKey: serviceKey(id.ComputerID, req.GetIdempotencyKey()),
				Actor: actor(ctx, g),
			})
			if err != nil {
				return nil, s.fail(ctx, "PurchaseTariff", err)
			}
			p, err := s.timePurchase(ctx, id, g, res, msgTariffBought)
			if err != nil {
				return nil, err
			}
			return &agentv1.PurchaseTariffResponse{Purchase: p}, nil
		})
}

// timePurchase — ответ sell(): балансы после покупки, минуты, сессия.
func (s *Server) timePurchase(ctx context.Context, id identity.Identity, g Guest, res billingsvc.PurchaseResult, msg string) (*agentv1.TimePurchase, error) {
	currency, tariff := "KZT", ""
	err := db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		ci, err := loadClub(ctx, q, id)
		if err != nil {
			return err
		}
		currency = ci.settings.currency
		tariff, err = sessionTariffName(ctx, q, id.ClubID, res.SessionID)
		return err
	})
	if err != nil {
		return nil, s.fail(ctx, "timePurchase", err)
	}
	sess := &agentv1.ActiveSession{SessionId: res.SessionID, UserId: g.UserID, EndsAt: timestamppb.New(res.SessionEndsAt), TariffName: tariff}
	return &agentv1.TimePurchase{
		Message:          msg,
		Balance:          &agentv1.Balance{MainTiyn: int64(res.NewBalance), BonusTiyn: int64(res.NewBonus), Currency: currency},
		MinutesAdded:     int32(min(res.MinutesAdded, 1<<31-1)), // #nosec G115 -- ограничено
		Session:          sess,
		ChargedMainTiyn:  int64(res.MainPaid),
		ChargedBonusTiyn: int64(res.BonusPaid),
	}, nil
}

// GetMinuteOffer — условия поминутной оплаты в зоне ПК (GET /api/guest/minutes, GuestMinutesController::show())
// и «на весь баланс» по основному балансу гостя (решение Q-008: минуты считает сервер, агент только показывает).
func (gs *guestService) GetMinuteOffer(ctx context.Context, _ *agentv1.GetMinuteOfferRequest) (*agentv1.GetMinuteOfferResponse, error) {
	s := gs.s
	ctx, id, g, err := s.guestCall(ctx, "api.guest.minutes")
	if err != nil {
		return nil, err
	}
	var out *agentv1.GetMinuteOfferResponse
	err = db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		o, pcRow, ci, err := minuteOfferFor(ctx, q, id, g.UserID)
		if err != nil {
			return err
		}
		out = minuteOffer(o, pcRow.ZoneName, ci.settings.currency)
		if o.WholeBalance && o.Available {
			b, err := q.GetMemberBalance(ctx, store.GetMemberBalanceParams{ClubID: id.ClubID, UserID: g.UserID})
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			// renderWholeBalance(): wholeMinutes = affordableMinutes(), цена — minutesPrice(hourCents, wholeMinutes).
			if minutes := pricing.WholeBalanceMinutes(o, money.Tiyn(b.Balance)); minutes > 0 {
				price, err := pricing.MinutesPrice(o.PricePerHour, minutes)
				if err != nil {
					return err
				}
				out.WholeBalanceMinutes, out.WholeBalancePriceTiyn = int32(minutes), int64(price) // #nosec G115 -- ≤ 720
			}
		}
		return nil
	})
	if err != nil {
		return nil, s.fail(ctx, "GetMinuteOffer", err)
	}
	return out, nil
}

func minuteOffer(o pricing.MinuteOffer, zone, currency string) *agentv1.GetMinuteOfferResponse {
	return &agentv1.GetMinuteOfferResponse{
		Available: o.Available, ZoneName: zone, PricePerHourTiyn: int64(o.PricePerHour),
		OriginalPricePerHourTiyn: int64(o.OriginalPricePerHour),
		BonusMaxPercent:          uint32(max(o.BonusMaxPercent, 0)), // #nosec G115 -- 0…100
		MixedPayment:             o.MixedPayment, Currency: currency,
		MinMinutes: int32(o.MinMinutes), MaxMinutes: int32(o.MaxMinutes), // #nosec G115 -- 1 и 720
		WholeBalance: o.WholeBalance, AutoBilling: o.AutoBilling,
		AutoBillingPauseMinutes: uint32(max(o.AutoBillingPauseMinutes, 0)), // #nosec G115 -- 0…60
	}
}

// PurchaseMinutes — покупка минут (POST /api/guest/minutes/purchase, purchaseMinutes()). «На весь баланс» и
// автобиллинг считает сервер (PlayWholeBalance, autobilling.go — решение Q-008).
func (gs *guestService) PurchaseMinutes(ctx context.Context, req *agentv1.PurchaseMinutesRequest) (*agentv1.PurchaseMinutesResponse, error) {
	s := gs.s
	ctx, id, g, err := s.guestCall(ctx, "api.guest.minutes.purchase")
	if err != nil {
		return nil, err
	}
	return once(ctx, s.idem, id.ComputerID, "PurchaseMinutes", req.GetIdempotencyKey(), req, &agentv1.PurchaseMinutesResponse{},
		func() (*agentv1.PurchaseMinutesResponse, error) {
			// GuestMinutesController::purchase(): minutes integer|min:1|max:720.
			if req.GetMinutes() < 1 || int64(req.GetMinutes()) > pricing.MaxMinutes {
				return nil, validation("minutes", msgMinutesRange)
			}
			res, err := s.o.Billing.PurchaseMinutes(ctx, billingsvc.MinutesPurchaseRequest{
				ClubID: id.ClubID, UserID: g.UserID, ComputerID: id.ComputerID, Minutes: int64(req.GetMinutes()),
				Balance: balanceChoice(req.GetBalanceSource()), IdempotencyKey: serviceKey(id.ComputerID, req.GetIdempotencyKey()),
				Actor: actor(ctx, g),
			})
			if err != nil {
				return nil, s.fail(ctx, "PurchaseMinutes", err)
			}
			p, err := s.timePurchase(ctx, id, g, res, msgMinutesBought)
			if err != nil {
				return nil, err
			}
			return &agentv1.PurchaseMinutesResponse{Purchase: p}, nil
		})
}

// PlayWholeBalance — «играть на весь баланс»: минуты считает сервер по основному балансу под блокировкой
// (billing PurchaseWholeBalance, решение Q-008). Старый агент считал их сам и покупал purchaseMinutes(…, 'main').
func (gs *guestService) PlayWholeBalance(ctx context.Context, req *agentv1.PlayWholeBalanceRequest) (*agentv1.PlayWholeBalanceResponse, error) {
	s := gs.s
	ctx, id, g, err := s.guestCall(ctx, "api.guest.minutes.purchase")
	if err != nil {
		return nil, err
	}
	return once(ctx, s.idem, id.ComputerID, "PlayWholeBalance", req.GetIdempotencyKey(), req, &agentv1.PlayWholeBalanceResponse{},
		func() (*agentv1.PlayWholeBalanceResponse, error) {
			res, err := s.o.Billing.PurchaseWholeBalance(ctx, billingsvc.WholeBalanceRequest{
				ClubID: id.ClubID, UserID: g.UserID, ComputerID: id.ComputerID,
				IdempotencyKey: serviceKey(id.ComputerID, req.GetIdempotencyKey()), Actor: actor(ctx, g),
			})
			if err != nil {
				return nil, s.fail(ctx, "PlayWholeBalance", err)
			}
			p, err := s.timePurchase(ctx, id, g, res, msgMinutesBought)
			if err != nil {
				return nil, err
			}
			return &agentv1.PlayWholeBalanceResponse{Purchase: p}, nil
		})
}

// GetAutoBilling — состояние автобиллинга гостя на этом ПК (покупает сервер, autobilling.go).
func (gs *guestService) GetAutoBilling(ctx context.Context, _ *agentv1.GetAutoBillingRequest) (*agentv1.GetAutoBillingResponse, error) {
	s := gs.s
	ctx, id, g, err := s.guestCall(ctx, "api.guest.minutes")
	if err != nil {
		return nil, err
	}
	st, err := s.autoBillingStatus(ctx, id, g)
	if err != nil {
		return nil, s.fail(ctx, "GetAutoBilling", err)
	}
	return &agentv1.GetAutoBillingResponse{Status: st}, nil
}

// ListPackages — сохранённые пакеты гостя (GET /api/guest/packages, packagesFor()).
func (gs *guestService) ListPackages(ctx context.Context, _ *agentv1.ListPackagesRequest) (*agentv1.ListPackagesResponse, error) {
	s := gs.s
	ctx, id, g, err := s.guestCall(ctx, "api.guest.packages")
	if err != nil {
		return nil, err
	}
	out := &agentv1.ListPackagesResponse{}
	err = db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		rows, err := q.GuestListPackages(ctx, store.GuestListPackagesParams{ClubID: id.ClubID, UserID: g.UserID, Now: s.o.Now()})
		if err != nil {
			return err
		}
		out.Packages = make([]*agentv1.TimePackage, 0, len(rows))
		for _, r := range rows {
			out.Packages = append(out.Packages, &agentv1.TimePackage{PackageId: r.ID, Name: r.Name,
				MinutesLeft: int32(min(r.MinutesLeft, 1<<31-1)), ExpiresAt: timestamppb.New(r.ExpiresAt)}) // #nosec G115 -- ограничено
		}
		return nil
	})
	if err != nil {
		return nil, s.fail(ctx, "ListPackages", err)
	}
	return out, nil
}

// StartPackage — запуск пакета на этом ПК без оплаты (POST /api/guest/packages/{package}/start, startPackage()).
func (gs *guestService) StartPackage(ctx context.Context, req *agentv1.StartPackageRequest) (*agentv1.StartPackageResponse, error) {
	s := gs.s
	ctx, id, g, err := s.guestCall(ctx, "api.guest.packages.start")
	if err != nil {
		return nil, err
	}
	return once(ctx, s.idem, id.ComputerID, "StartPackage", req.GetIdempotencyKey(), req, &agentv1.StartPackageResponse{},
		func() (*agentv1.StartPackageResponse, error) {
			res, err := s.o.Billing.StartPackage(ctx, billingsvc.PackageStartRequest{
				ClubID: id.ClubID, UserID: g.UserID, PackageID: req.GetPackageId(), ComputerID: id.ComputerID,
				IdempotencyKey: serviceKey(id.ComputerID, req.GetIdempotencyKey()), Actor: actor(ctx, g),
			})
			if err != nil {
				return nil, s.fail(ctx, "StartPackage", err)
			}
			p, err := s.timePurchase(ctx, id, g, res, msgPackageStarted)
			if err != nil {
				return nil, err
			}
			return &agentv1.StartPackageResponse{Purchase: p}, nil
		})
}

// EndSession — досрочное завершение сессии гостем (POST /api/club-computers/{computer}/session/end,
// ClubComputerSessions::end() → SessionSettlement::settle()).
func (gs *guestService) EndSession(ctx context.Context, req *agentv1.EndSessionRequest) (*agentv1.EndSessionResponse, error) {
	s := gs.s
	ctx, id, g, err := s.guestCall(ctx, "club-computers")
	if err != nil {
		return nil, err
	}
	return once(ctx, s.idem, id.ComputerID, "EndSession", req.GetIdempotencyKey(), req, &agentv1.EndSessionResponse{},
		func() (*agentv1.EndSessionResponse, error) {
			if req.GetSessionId() == "" {
				return nil, rpcError(codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_NO_ACTIVE_SESSION,
					billingsvc.ErrNoSuchSession.Message, nil, "session_id")
			}
			res, err := s.o.Billing.EndSession(ctx, billingsvc.EndSessionRequest{
				ClubID: id.ClubID, ComputerID: id.ComputerID, SessionID: req.GetSessionId(), Actor: actor(ctx, g),
			})
			if err != nil {
				return nil, s.fail(ctx, "EndSession", err)
			}
			out := &agentv1.EndSessionResponse{RefundedMainTiyn: int64(res.RefundMain), RefundedBonusTiyn: int64(res.RefundBonus),
				KeptMinutes: int32(min(res.KeptMinutes, 1<<31-1))} // #nosec G115 -- ограничено
			err = db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
				ci, err := loadClub(ctx, q, id)
				if err != nil {
					return err
				}
				out.Balance, err = balanceOf(ctx, q, id.ClubID, g.UserID, ci.settings.currency)
				return err
			})
			if err != nil {
				return nil, s.fail(ctx, "EndSession", err)
			}
			return out, nil
		})
}

// GetSeatMap — TODO: карта пересадки (SeatTransfers::map()) зависит от «ПК в сети» по last_seen_at, которого
// больше нет (open-questions/stage2-agent-stream.md AS-2) — перенос вместе с решением GG-7.
func (gs *guestService) GetSeatMap(ctx context.Context, _ *agentv1.GetSeatMapRequest) (*agentv1.GetSeatMapResponse, error) {
	if _, _, _, err := gs.s.guestCall(ctx, "api.guest.seats.index"); err != nil {
		return nil, err
	}
	return nil, status.Error(codes.Unimplemented, "seat map is not implemented yet")
}

// TransferSeat — пересадка остатка сессии на другой ПК (POST /api/guest/seat-transfer, SeatTransfers::transfer()).
func (gs *guestService) TransferSeat(ctx context.Context, req *agentv1.TransferSeatRequest) (*agentv1.TransferSeatResponse, error) {
	s := gs.s
	ctx, id, g, err := s.guestCall(ctx, "api.guest.seat-transfer")
	if err != nil {
		return nil, err
	}
	return once(ctx, s.idem, id.ComputerID, "TransferSeat", req.GetIdempotencyKey(), req, &agentv1.TransferSeatResponse{},
		func() (*agentv1.TransferSeatResponse, error) {
			if err := s.requireFeature(ctx, id, featureSelfTransfer); err != nil {
				return nil, err
			}
			res, err := s.o.Billing.TransferSeat(ctx, billingsvc.TransferRequest{ClubID: id.ClubID, UserID: g.UserID,
				FromComputerID: id.ComputerID, TargetComputerID: req.GetTargetComputerId()})
			if err != nil {
				return nil, s.fail(ctx, "TransferSeat", err)
			}
			out := &agentv1.TransferSeatResponse{Session: &agentv1.ActiveSession{SessionId: res.SessionID, UserId: g.UserID,
				EndsAt: timestamppb.New(res.SessionEndsAt)}}
			err = db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
				t, err := q.GuestComputer(ctx, store.GuestComputerParams{ClubID: id.ClubID, ID: res.ComputerID})
				if err != nil {
					return err
				}
				name := displayName(t.DisplayName, t.DeviceName, t.Number)
				out.Target = computerRef(t.ID, t.Number, name, t.ZoneName)
				out.Message = "Сессия перенесена на ПК " + name + ". Войдите там своей учётной записью."
				return nil
			})
			if err != nil {
				return nil, s.fail(ctx, "TransferSeat", err)
			}
			// Гость пересел: токен этого ПК больше не нужен (на новом ПК он войдёт заново).
			return out, nil
		})
}

func computerRef(id, number int64, name, zone string) *agentv1.ComputerRef {
	return &agentv1.ComputerRef{ComputerId: id, Number: itoa(number), DisplayName: name, ZoneName: zone}
}

// requireFeature — GuestFeatures::ensure(): функция выключена в клубе → 403 «Эта возможность отключена в клубе.»
func (s *Server) requireFeature(ctx context.Context, id identity.Identity, feature string) error {
	var on bool
	err := db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		ci, err := loadClub(ctx, q, id)
		if err != nil {
			return err
		}
		on = ci.settings.feature(feature)
		return nil
	})
	if err != nil {
		return s.fail(ctx, "requireFeature", err)
	}
	if !on {
		return featureDisabled()
	}
	return nil
}
