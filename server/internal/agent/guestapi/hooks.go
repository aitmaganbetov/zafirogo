package guestapi

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/identity"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/stream"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Hooks — реализации хуков стрима: stream.WelcomeProvider (ClubConfig, версия переводов, срок продления
// сертификата) и stream.EventHandler (события агента, свежая загрузка Windows). Передаются в stream.Options
// (Welcome, Events) до stream.New; сервис стрима им не нужен.
type Hooks struct {
	s *Server
}

var (
	_ stream.WelcomeProvider = (*Hooks)(nil)
	_ stream.EventHandler    = (*Hooks)(nil)
)

// NewHooks — хуки поверх тех же Options, что и Register (Stream может быть nil: хуки создаются раньше стрима).
func NewHooks(o Options) (*Hooks, error) {
	o.Stream = nil
	s, err := New(o)
	if err != nil {
		return nil, err
	}
	return &Hooks{s: s}, nil
}

// Tokens — хранилище токенов гостя хуков (тот же Valkey и префикс, что у Server): enroll.Options.GuestTokens
// отзывает им токен гостя ПК при отзыве сертификата (open-questions/stage5-agent.md AG-5).
func (h *Hooks) Tokens() *Tokens { return h.s.tokens }

// Hooks — хуки, которые делят хранилище токенов с этим Server.
func (s *Server) Hooks() *Hooks { return &Hooks{s: s} }

// CompleteWelcome дополняет Welcome: ClubConfig целиком (блок club ответа heartbeat), версия переводов
// (TranslationCatalog::version()) и срок продления сертификата (2/3 срока от выпуска, pki.Issue).
func (h *Hooks) CompleteWelcome(ctx context.Context, id identity.Identity, w *agentv1.Welcome) error {
	s := h.s
	return db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		ci, err := loadClub(ctx, q, id)
		if err != nil {
			return err
		}
		langs, err := q.GuestEnabledLanguages(ctx, store.GuestEnabledLanguagesParams{ClubID: id.ClubID, ComputerID: id.ComputerID})
		if err != nil {
			return err
		}
		w.Config = s.clubConfig(ci, langs)
		if ses := w.GetSession(); ses != nil && ses.GetSessionId() != "" && ses.GetTariffName() == "" {
			if ses.TariffName, err = sessionTariffName(ctx, q, id.ClubID, ses.GetSessionId()); err != nil {
				return err
			}
		}
		if w.TranslationsVersion, err = q.GuestTranslationsVersion(ctx, store.GuestTranslationsVersionParams{ClubID: id.ClubID, ComputerID: id.ComputerID}); err != nil {
			return err
		}
		if id.CertSerial != "" {
			ct, err := q.GuestCertificateTimes(ctx, store.GuestCertificateTimesParams{ClubID: id.ClubID, Serial: id.CertSerial, ComputerID: id.ComputerID})
			switch {
			case err == nil:
				w.CertificateRenewAfter = timestamppb.New(ct.IssuedAt.Add(ct.NotAfter.Sub(ct.IssuedAt) * 2 / 3))
			case !errors.Is(err, pgx.ErrNoRows):
				return err
			}
		}
		return nil
	})
}

// FreshBoot — новый Hello.boot_id: время, проданное на кассе гостю без аккаунта, не переживает перезагрузку ПК.
// Laravel: app/Services/ClubComputerSessions.php resetGuestSession() (стр. 162–175) — только гостевая
// оплаченная сессия; журнал computer.guest_session_reset от имени владельца клуба. Платная сессия клиента
// не трогается (GS-077).
func (h *Hooks) FreshBoot(ctx context.Context, id identity.Identity) error {
	s := h.s
	now := s.o.Now()
	return db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		row, err := q.GuestResetGuestSession(ctx, store.GuestResetGuestSessionParams{Now: &now, ClubID: id.ClubID, ID: id.ComputerID})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		owner, err := q.GuestClubOwnerID(ctx, id.ClubID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		computer := displayName(row.DisplayName, row.DeviceName, row.Number)
		meta := activity.RequestMeta{IP: "127.0.0.1"} // системное действие при подключении ПК
		if ip := peerIP(ctx); ip != "" {
			meta.IP = ip
		}
		_, err = activity.Record(ctx, q, activity.Entry{ClubID: id.ClubID, Event: "computer.guest_session_reset",
			Description: "Гостевая сессия на ПК " + computer + " сброшена: ПК перезагружен.", UserID: owner,
			SubjectType: subjectComputer, SubjectID: id.ComputerID, Meta: &meta, At: now})
		return err
	})
}

// HandleEvent — события агента после дедупликации по event_id:
//   - SessionEnded с причиной GUEST — гость завершил сессию на ПК (кнопка в агенте, если RPC EndSession не
//     дошёл): завершение с расчётом, как POST /session/end владельца (ClubComputerSessions::end()). Другие
//     причины сессию на сервере не меняют: время ведёт сервер (истечение — воркер, пересадка и персонал — свои
//     RPC/панель), выключение ПК сессию не обрывает («an agent state change … does not erase it»);
//   - GuestSignedOut — гость вышел на ПК: токен гостя этого ПК отзывается (как POST /api/auth/logout);
//   - SessionEnded с причиной EXPIRED — автобиллинг покупает сразу (autobilling.go);
//   - остальное — в лог (ошибки агента, обновления, техрежим).
func (h *Hooks) HandleEvent(ctx context.Context, id identity.Identity, ev *agentv1.AgentEvent) error {
	s := h.s
	switch k := ev.GetKind().(type) {
	case *agentv1.AgentEvent_SessionEnded:
		if k.SessionEnded.GetReason() == agentv1.SessionEndReason_SESSION_END_REASON_EXPIRED {
			// Агент сам увидел конец сессии по часам сервера: автобиллинг покупает сразу (autoAfterTariff), не дожидаясь
			// воркера истечения.
			s.abSessionEnded(ctx, id.ComputerID, agentv1.SessionEndReason_SESSION_END_REASON_EXPIRED)
			return nil
		}
		if k.SessionEnded.GetReason() != agentv1.SessionEndReason_SESSION_END_REASON_GUEST || k.SessionEnded.GetSessionId() == "" {
			return nil
		}
		return h.endByGuest(ctx, id, k.SessionEnded.GetSessionId())
	case *agentv1.AgentEvent_GuestSignedOut:
		if err := s.tokens.RevokeComputer(ctx, id.ComputerID, k.GuestSignedOut.GetUserId()); err != nil {
			return err
		}
	case *agentv1.AgentEvent_Error:
		s.o.Log.WarnContext(ctx, "agent error", "club_id", id.ClubID, "computer_id", id.ComputerID,
			"component", k.Error.GetComponent(), "code", k.Error.GetErrorCode().String(), "message", k.Error.GetMessage())
	case *agentv1.AgentEvent_UpdateResult:
		s.o.Log.InfoContext(ctx, "agent update result", "computer_id", id.ComputerID, "version", k.UpdateResult.GetVersion(),
			"success", k.UpdateResult.GetSuccess(), "code", k.UpdateResult.GetErrorCode().String())
	}
	return nil
}

// endByGuest — сессия владельца на этом ПК завершается с возвратом (billing.EndSession, не сотрудник).
// Сессии уже нет или она другая — событие подтверждается без действий.
func (h *Hooks) endByGuest(ctx context.Context, id identity.Identity, sessionID string) error {
	s := h.s
	var owner int64
	err := db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		pcRow, err := q.GuestComputer(ctx, store.GuestComputerParams{ClubID: id.ClubID, ID: id.ComputerID})
		if err != nil {
			return err
		}
		if pcRow.CurrentSessionID != nil && *pcRow.CurrentSessionID == sessionID && pcRow.CurrentSessionUserID != nil {
			owner = *pcRow.CurrentSessionUserID
		}
		return nil
	})
	if err != nil || owner == 0 {
		return err
	}
	_, err = s.o.Billing.EndSession(withRequestMeta(ctx), billingsvc.EndSessionRequest{ClubID: id.ClubID, ComputerID: id.ComputerID,
		SessionID: sessionID, Actor: billingsvc.Actor{UserID: owner, Kind: "agent"}})
	var be *billingsvc.Error
	if errors.As(err, &be) && be.Code == billingsvc.ErrNoSuchSession.Code {
		return nil
	}
	return err
}

// ConfigChanged — команда ConfigChanged с актуальным ClubConfig (для панели настроек клуба: после сохранения
// раздела «Гости»/«Клуб»/«Финансы» вызывающий рассылает её ПК клуба).
func (h *Hooks) ConfigChanged(ctx context.Context, id identity.Identity) (*agentv1.Command, error) {
	w := &agentv1.Welcome{}
	if err := h.CompleteWelcome(ctx, id, w); err != nil {
		return nil, err
	}
	return &agentv1.Command{Kind: &agentv1.Command_ConfigChanged{ConfigChanged: &agentv1.ConfigChanged{Config: w.GetConfig()}}}, nil
}
