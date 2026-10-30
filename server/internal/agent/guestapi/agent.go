package guestapi

import (
	"context"
	"regexp"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/stream"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// AgentService — AgentService целиком: стрим Connect — встроенный *stream.Service (A3), GetTranslations и
// VerifyStaffPin — здесь (open-questions/stage2-agent-stream.md: «оркестратор собирает AgentServiceServer
// встраиванием *Service»).
type AgentService struct {
	*stream.Service
	g *Server
}

var _ agentv1.AgentServiceServer = (*AgentService)(nil)

// Connect — стрим агента (stream.Service). Без сервиса стрима — Unimplemented.
func (a *AgentService) Connect(st agentv1.AgentService_ConnectServer) error {
	if a.Service == nil {
		return status.Error(codes.Unimplemented, "agent stream is not configured")
	}
	return a.Service.Connect(st)
}

// DefaultStaffMinutes — ClubComputerSessions::DEFAULT_STAFF_MINUTES.
const DefaultStaffMinutes = 15

const (
	msgPinNotConfigured = "PIN-код не задан. Задайте его в настройках клуба в панели Zafiro." // AgentExitPinController
	msgInvalidPin       = "Неверный PIN-код."
	msgPinRequired      = "Введите PIN-код."
	msgLocaleUnknown    = "Язык не найден."
)

var localeRe = regexp.MustCompile(`^[a-z]{2,8}$`) // routes/api.php: locale [a-z]{2,8}

// GetTranslations — тексты агента на одном языке с правками суперадмина (GET /api/agent/translations/{locale},
// AgentTranslationController, TranslationCatalog::lines($locale, 'agent')). Лимит — agent-translations (600/мин на IP).
func (a *AgentService) GetTranslations(ctx context.Context, req *agentv1.GetTranslationsRequest) (*agentv1.GetTranslationsResponse, error) {
	s := a.g
	ctx, id, err := s.call(ctx, ratelimit.AgentTranslations, nil)
	if err != nil {
		return nil, err
	}
	locale := req.GetLocale()
	if !localeRe.MatchString(locale) {
		return nil, validation("locale", msgLocaleUnknown)
	}
	out := &agentv1.GetTranslationsResponse{Locale: locale}
	err = db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		enabled, err := q.GuestLanguageEnabled(ctx, store.GuestLanguageEnabledParams{Locale: locale, ClubID: id.ClubID, ComputerID: id.ComputerID})
		if err != nil {
			return err
		}
		if enabled == nil || !*enabled { // abort_unless(in_array($locale, enabledCodes()), 404)
			return rpcError(codes.NotFound, agentv1.ErrorCode_ERROR_CODE_VALIDATION_FAILED, msgLocaleUnknown, nil, "locale")
		}
		if out.Version, err = q.GuestTranslationsVersion(ctx, store.GuestTranslationsVersionParams{ClubID: id.ClubID, ComputerID: id.ComputerID}); err != nil {
			return err
		}
		if req.GetKnownVersion() > 0 && req.GetKnownVersion() == out.Version {
			out.NotModified = true
			return nil
		}
		rows, err := q.GuestAgentTranslations(ctx, store.GuestAgentTranslationsParams{Locale: locale, ClubID: id.ClubID, ComputerID: id.ComputerID})
		if err != nil {
			return err
		}
		// Строк группы agent в базе нет — пустой словарь: агент показывает встроенные тексты
		// (в Laravel — shippedGroup($locale, 'agent') из файлов lang/, их в Go нет).
		out.Lines = make(map[string]string, len(rows))
		for _, r := range rows {
			out.Lines[r.Key] = r.Value
		}
		return nil
	})
	if err != nil {
		return nil, s.fail(ctx, "GetTranslations", err)
	}
	return out, nil
}

// VerifyStaffPin — PIN сотрудника для выхода из агента / техрежима (POST /api/agent/exit-pin,
// AgentExitPinController::__invoke(), лимит agent-exit-pin: 5 за 5 минут на ПК и 100 в час на клуб).
// При верном PIN ПК переходит «в руки сотрудника» на agent_staff_minutes (ClubComputerSessions::startStaffVisit(..., 'pin')).
// Если в метаданных есть токен гостя этого ПК — в журнал пишется, при ком закрыли агент (agent.staff_exit /
// agent.exit_pin_failed); без него — только системный лог, как в старом.
func (a *AgentService) VerifyStaffPin(ctx context.Context, req *agentv1.VerifyStaffPinRequest) (*agentv1.VerifyStaffPinResponse, error) {
	s := a.g
	ctx, id, err := s.call(ctx, ratelimit.AgentExitPin, nil)
	if err != nil {
		return nil, err
	}
	if req.GetPin() == "" || len(req.GetPin()) > 16 { // pin required|string|max:16
		return nil, validation("pin", msgPinRequired)
	}
	var ci clubInfo
	err = db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		var err error
		ci, err = loadClub(ctx, q, id)
		return err
	})
	if err != nil {
		return nil, s.fail(ctx, "VerifyStaffPin", err)
	}
	if ci.row.AgentExitPinHash == nil || *ci.row.AgentExitPinHash == "" {
		return nil, rpcError(codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_PIN_NOT_CONFIGURED, msgPinNotConfigured, nil, "")
	}
	_, verr := s.o.Passwords.Verify(ctx, req.GetPin(), *ci.row.AgentExitPinHash)
	valid := verr == nil

	// Гость, чей (неистёкший) токен держит агент этого ПК, если он член клуба (signedInUser()).
	var user *store.GuestUserInClubRow
	if bearer(ctx) != "" {
		if g, err := s.tokens.FromContext(ctx, id); err == nil {
			_ = db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
				if u, err := q.GuestUserInClub(ctx, store.GuestUserInClubParams{ClubID: id.ClubID, UserID: g.UserID}); err == nil {
					user = &u
				}
				return nil
			})
		}
	}
	now := s.o.Now()
	minutes := int64(ci.row.AgentStaffMinutes)
	until := now.Add(time.Duration(minutes) * time.Minute)
	err = db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		pcRow, err := q.GuestComputer(ctx, store.GuestComputerParams{ClubID: id.ClubID, ID: id.ComputerID})
		if err != nil {
			return err
		}
		computer := displayName(pcRow.DisplayName, pcRow.DeviceName, pcRow.Number)
		if user == nil {
			if valid {
				s.o.Log.InfoContext(ctx, "Agent closed with staff PIN", "club_id", id.ClubID, "computer_id", id.ComputerID)
			} else {
				s.o.Log.WarnContext(ctx, "Wrong agent exit PIN", "club_id", id.ClubID, "computer_id", id.ComputerID)
			}
		} else {
			event, desc := "agent.exit_pin_failed", "Неверный PIN-код выхода на ПК "+computer
			if valid {
				event, desc = "agent.staff_exit", "Агент на ПК "+computer+" закрыт по PIN-коду сотрудника"
			}
			if err := s.record(ctx, q, activity.Entry{ClubID: id.ClubID, Event: event, Description: desc, UserID: user.ID,
				SubjectType: subjectComputer, SubjectID: id.ComputerID, Details: map[string]any{"signed_in_user": user.Name}}); err != nil {
				return err
			}
		}
		if !valid {
			return nil
		}
		// PIN общий: сотрудник известен, только если это он вошёл на ПК.
		var staff *int64
		if user != nil && pcStaffRoles[user.Role] {
			staff = &user.ID
		}
		kind := "pin"
		return q.GuestStartStaffVisit(ctx, store.GuestStartStaffVisitParams{StaffUserID: staff, Kind: &kind, Until: &until,
			Now: &now, ClubID: id.ClubID, ID: id.ComputerID})
	})
	if err != nil {
		return nil, s.fail(ctx, "VerifyStaffPin", err)
	}
	if !valid {
		return nil, rpcError(codes.PermissionDenied, agentv1.ErrorCode_ERROR_CODE_INVALID_PIN, msgInvalidPin, nil, "pin")
	}
	return &agentv1.VerifyStaffPinResponse{StaffMinutes: uint32(max(minutes, 0)), Until: timestamppb.New(until)}, nil // #nosec G115 -- integer столбец
}
