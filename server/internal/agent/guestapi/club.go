package guestapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/durationpb"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/identity"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/bonus"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/pricing"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Возможности гостя (GuestFeatures::SETTINGS: ключ → настройка раздела guests).
const (
	featureQRLogin         = "qr_login"
	featureSelfTransfer    = "self_transfer"
	featureFriends         = "friends"
	featureBalanceTransfer = "balance_transfer"
)

var guestFeatureSettings = map[string]string{
	featureQRLogin:         "show_qr_code_on_login",
	featureSelfTransfer:    "allow_guest_self_transfer",
	featureFriends:         "allow_adding_friends",
	featureBalanceTransfer: "allow_balance_transfer_to_friends",
}

// DefaultIdleLogoutMinutes — GuestFeatures::DEFAULT_IDLE_LOGOUT_MINUTES.
const DefaultIdleLogoutMinutes = 5

// defaultClubLocale — LocaleResolver::DEFAULT_CLUB_LOCALE.
const defaultClubLocale = "ru"

// clubSettings — то, что агент и гость читают из clubs.settings (CurrentClub::settings()).
type clubSettings struct {
	root     map[string]json.RawMessage
	section  map[string]map[string]json.RawMessage
	rules    pricing.PaymentRules
	bonus    bonus.Settings
	currency string
}

func parseClubSettings(raw []byte) clubSettings {
	cs := clubSettings{section: map[string]map[string]json.RawMessage{}, currency: "KZT"}
	// CurrentClub::settings(): некорректный JSON → [] (все значения по умолчанию).
	_ = json.Unmarshal(raw, &cs.root)
	for _, name := range []string{"guests", "club", "finances"} {
		var m map[string]json.RawMessage
		if json.Unmarshal(cs.root[name], &m) == nil {
			cs.section[name] = m
		}
	}
	fin, err := pricing.ParseFinances(raw)
	if err != nil {
		fin = nil
	}
	cs.rules = pricing.RulesFromFinances(fin)
	if b, err := bonus.ParseSettings(raw); err == nil {
		cs.bonus = b
	}
	// data_get($settings, 'finances.currency', 'KZT').
	var cur string
	if v, ok := cs.section["finances"]["currency"]; ok && json.Unmarshal(v, &cur) == nil && cur != "" {
		cs.currency = cur
	}
	return cs
}

func (cs clubSettings) value(section, key string) (json.RawMessage, bool) {
	v, ok := cs.section[section][key]
	if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
		return nil, false // ?? — null и отсутствие одинаковы
	}
	return v, true
}

// phpBool — filter_var($v, FILTER_VALIDATE_BOOLEAN): true для true, 1, "1", "true", "on", "yes" (без учёта
// регистра и пробелов по краям); всё остальное — false.
func phpBool(v json.RawMessage) bool {
	var b bool
	if json.Unmarshal(v, &b) == nil {
		return b
	}
	var n json.Number
	if json.Unmarshal(v, &n) == nil {
		f, err := n.Float64()
		return err == nil && f == 1
	}
	var s string
	if json.Unmarshal(v, &s) == nil {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "1", "true", "on", "yes":
			return true
		}
	}
	return false
}

// phpNumber — is_numeric($v) ? (float) $v : ok=false.
func phpNumber(v json.RawMessage) (float64, bool) {
	var n json.Number
	if json.Unmarshal(v, &n) == nil {
		f, err := n.Float64()
		return f, err == nil
	}
	var s string
	if json.Unmarshal(v, &s) == nil && money.PHPIsNumeric(s) {
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		return f, err == nil
	}
	return 0, false
}

// feature — GuestFeatures::enabled(): filter_var(settings.guests.<настройка> ?? false, FILTER_VALIDATE_BOOLEAN).
func (cs clubSettings) feature(name string) bool {
	v, ok := cs.value("guests", guestFeatureSettings[name])
	return ok && phpBool(v)
}

// shopEnabled — GuestFeatures::shopEnabled(): settings.club.agent_shop_enabled ?? true.
func (cs clubSettings) shopEnabled() bool {
	v, ok := cs.value("club", "agent_shop_enabled")
	return !ok || phpBool(v)
}

// idleLogoutMinutes — GuestFeatures::idleLogoutMinutes(): is_numeric ? max(0, (int) ceil((float) m)) : 5.
func (cs clubSettings) idleLogoutMinutes() uint32 {
	v, ok := cs.value("club", "agent_idle_logout_minutes")
	if !ok {
		return DefaultIdleLogoutMinutes
	}
	f, ok := phpNumber(v)
	if !ok {
		return DefaultIdleLogoutMinutes
	}
	return clampUint32(math.Ceil(f))
}

// afterSession — ClubComputerController::afterSessionAction(): action none|restart|shutdown,
// delay_seconds = is_numeric(m) ? (int) round(max(0, (float) m) * 60) : 30 (по умолчанию 0.5 мин).
func (cs clubSettings) afterSession() (agentv1.AfterSessionAction, time.Duration) {
	action := agentv1.AfterSessionAction_AFTER_SESSION_ACTION_NONE
	if v, ok := cs.value("club", "pc_action_after_session_end"); ok {
		var s string
		if json.Unmarshal(v, &s) == nil {
			switch s {
			case "restart":
				action = agentv1.AfterSessionAction_AFTER_SESSION_ACTION_RESTART
			case "shutdown":
				action = agentv1.AfterSessionAction_AFTER_SESSION_ACTION_SHUTDOWN
			}
		}
	}
	seconds := 30.0 // 0.5 мин
	if v, ok := cs.value("club", "pc_restart_after_session_end"); ok {
		if f, ok := phpNumber(v); ok {
			seconds = money.PHPRound(math.Max(0, f)*60, 0)
		} else {
			seconds = 30
		}
	}
	return action, time.Duration(clampUint32(seconds)) * time.Second
}

func clampUint32(f float64) uint32 {
	switch {
	case f != f || f <= 0: // NaN, отрицательные
		return 0
	case f >= math.MaxUint32:
		return math.MaxUint32
	}
	return uint32(f)
}

// guestFeatures — блок guest_features для агента: GuestFeatures::all() + shop.
func (cs clubSettings) guestFeatures() *agentv1.GuestFeatures {
	return &agentv1.GuestFeatures{
		QrLogin:         cs.feature(featureQRLogin),
		SelfTransfer:    cs.feature(featureSelfTransfer),
		Friends:         cs.feature(featureFriends),
		BalanceTransfer: cs.feature(featureBalanceTransfer),
		Shop:            cs.shopEnabled(),
	}
}

// clubInfo — клуб ПК: название, настройки, часовой пояс, PIN выхода.
type clubInfo struct {
	id       int64
	row      store.GuestClubForAgentRow
	settings clubSettings
	loc      *time.Location
}

// loadClub — клуб ПК (в транзакции клуба).
func loadClub(ctx context.Context, q *store.Queries, id identity.Identity) (clubInfo, error) {
	row, err := q.GuestClubForAgent(ctx, store.GuestClubForAgentParams{ClubID: id.ClubID, ComputerID: id.ComputerID})
	if errors.Is(err, pgx.ErrNoRows) {
		return clubInfo{}, rpcError(codes.NotFound, agentv1.ErrorCode_ERROR_CODE_COMPUTER_NOT_FOUND, msgNotFound, nil, "")
	}
	if err != nil {
		return clubInfo{}, err
	}
	ci := clubInfo{id: id.ClubID, row: row, settings: parseClubSettings(row.Settings), loc: time.UTC}
	// CurrentClub::localTimezone(): пустая строка → UTC.
	if row.Timezone != "" {
		if loc, err := time.LoadLocation(row.Timezone); err == nil {
			ci.loc = loc
		}
	}
	return ci, nil
}

// subscriptionActive — ClubSubscription::isActive() (app/Models/ClubSubscription.php стр. 31–35, BR-130).
func subscriptionActive(row store.GuestClubSubscriptionRow, now time.Time) bool {
	switch row.Status {
	case "active":
		return row.CurrentPeriodEndsAt == nil || row.CurrentPeriodEndsAt.After(now)
	case "trialing":
		return row.TrialEndsAt != nil && row.TrialEndsAt.After(now)
	}
	return false
}

// clubConfig — ClubConfig для Welcome/ConfigChanged (блок club ответа heartbeat, ClubComputerController::heartbeatResponse()).
func (s *Server) clubConfig(ci clubInfo, languages []store.GuestEnabledLanguagesRow) *agentv1.ClubConfig {
	cs := ci.settings
	action, delay := cs.afterSession()
	cfg := &agentv1.ClubConfig{
		ClubName:                ci.row.Name,
		DefaultLocale:           clubLocale(ci.row.Locale, languages),
		AfterSessionAction:      action,
		AfterSessionDelay:       durationpb.New(delay),
		GuestFeatures:           cs.guestFeatures(),
		IdleLogoutMinutes:       cs.idleLogoutMinutes(),
		Currency:                cs.currency,
		StaffMinutes:            uint32(max(ci.row.AgentStaffMinutes, 0)), // #nosec G115 -- неотрицательное после max
		AutoBilling:             cs.rules.AutoBilling,
		AutoBillingPauseMinutes: uint32(max(cs.rules.AutoBillingPauseMinutes, 0)), // #nosec G115 -- 0…60 (PaymentRules)
	}
	for _, l := range languages {
		cfg.Languages = append(cfg.Languages, &agentv1.Language{Code: l.Code, Name: l.Name})
	}
	// Club::logoUrl() / signInImageUrl(): route('club-logos.show', [$club, basename($path)]).
	if ci.row.LogoPath != nil && *ci.row.LogoPath != "" {
		cfg.LogoUrl = s.o.PublicBaseURL + "/club-logos/" + strconv.FormatInt(ci.id, 10) + "/" + basename(*ci.row.LogoPath)
	}
	if ci.row.SigninImagePath != nil && *ci.row.SigninImagePath != "" {
		cfg.SigninImageUrl = s.o.PublicBaseURL + "/club-signin-images/" + strconv.FormatInt(ci.id, 10) + "/" + basename(*ci.row.SigninImagePath)
	}
	return cfg
}

func basename(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// clubLocale — LocaleResolver::forClub(): язык клуба, если он включён, иначе ru (если включён) или первый включённый.
func clubLocale(locale *string, languages []store.GuestEnabledLanguagesRow) string {
	code := defaultClubLocale
	if locale != nil && *locale != "" {
		code = *locale
	}
	if len(languages) == 0 {
		return code // языков в базе нет (TranslationCatalog::defaultMeta(): ru, en) — язык клуба как есть
	}
	enabled := func(c string) bool {
		for _, l := range languages {
			if l.Code == c {
				return true
			}
		}
		return false
	}
	if enabled(code) {
		return code
	}
	if enabled(defaultClubLocale) {
		return defaultClubLocale
	}
	return languages[0].Code
}
