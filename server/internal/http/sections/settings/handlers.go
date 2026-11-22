package settings

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/bonus"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	"github.com/aitmaganbetov/zafirogo/server/internal/clubtz"
	settingsapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/settings"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/shelltheme"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Тексты SettingsController / LanguageController (ключи перевода).
const (
	msgSaved          = "Настройки сохранены"
	msgTimezoneSaved  = "Часовой пояс сохранён."
	msgLanguageSaved  = "Язык интерфейса изменён."
	msgPinSaved       = "PIN-код сохранён."
	msgPinDeleted     = "PIN-код удалён."
	msgStaffTimeSaved = "Время обслуживания сохранено."
	msgPinFormat      = "PIN-код — от 4 до 8 цифр."

	logSettings  = "Изменены настройки: :fields"
	logTimezone  = "Часовой пояс клуба изменён: :from → :to"
	logLanguage  = "Язык клуба изменён: :from → :to"
	logPinSet    = "PIN-код выхода из агента изменён"
	logPinDelete = "PIN-код выхода из агента удалён"
	logStaffTime = "Время обслуживания ПК изменено: :from → :to мин"

	subjectClub = `App\Models\Club`
	// defaultLocale — LocaleResolver::DEFAULT_CLUB_LOCALE.
	defaultLocale = "ru"
)

// GetClubSettings — club(), finances(), guests(): loadSettings() и данные карточек «Настройки клуба».
func (h *handlers) GetClubSettings(ctx context.Context, _ settingsapi.GetClubSettingsRequestObject) (settingsapi.GetClubSettingsResponseObject, error) {
	p := httpx.Principal(ctx)
	var (
		row  store.SettingsClubRow
		prof store.ClubProfileGetRow
	)
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		if row, err = q.SettingsClub(ctx, p.ClubID); err != nil {
			return err
		}
		prof, err = q.ClubProfileGet(ctx, p.ClubID)
		return err
	})
	if err != nil {
		return nil, err
	}
	now := h.d.NowFunc()
	loc := clubLocation(row.Timezone)
	kz, all := timezoneOptions(now)
	out := settingsapi.ClubSettingsPage{
		ClubName:            row.Name,
		Settings:            settingsView(loadSettings(row.Settings)),
		Timezone:            row.Timezone,
		TimezonesKazakhstan: kz,
		TimezonesAll:        all,
		ClubTime:            now.In(loc).Format("02.01.2006 15:04"),
		AgentStaffMinutes:   row.AgentStaffMinutes,
		CanManageAgentPin:   p.Can("workers.update"),
		// Карточка «Город и адрес» (SettingsController::club(): 'cities' => Cities::options()).
		City:    prof.City,
		Address: prof.Address,
		Cities:  h.cityOptions(ctx),
		// Раздел «Агент ПК»: свотчи тем (club.blade.php: ShellThemes::names() + swatch()).
		ShellThemes: shellThemeOptions(),
	}
	if row.PinSet {
		out.AgentPinSetAt = row.AgentExitPinChangedAt
	}
	if row.LogoPath != nil && *row.LogoPath != "" {
		u := imageURL(kindLogo, p.ClubID, *row.LogoPath)
		out.LogoUrl = &u
	}
	if row.SigninImagePath != nil && *row.SigninImagePath != "" {
		u := imageURL(kindSignin, p.ClubID, *row.SigninImagePath)
		out.SigninImageUrl = &u
	}
	return settingsapi.GetClubSettings200JSONResponse(out), nil
}

// shellThemeValue — выбранная тема для формы: old('agent_shell_theme', $settings['club']['agent_shell_theme'] ??
// ShellThemes::DEFAULT) (null — стандартная; иное значение — как есть, ни одна тема не отмечена).
func shellThemeValue(r json.RawMessage) string {
	if jsonKind(r) == 'n' {
		return shelltheme.Default
	}
	return phpString(r)
}

// shellThemeOptions — темы в порядке ShellThemes::names() с цветами ShellThemes::swatch().
func shellThemeOptions() []settingsapi.ShellThemeOption {
	keys := shelltheme.Keys()
	out := make([]settingsapi.ShellThemeOption, 0, len(keys))
	for _, k := range keys {
		sw := shelltheme.Swatch(k)
		out = append(out, settingsapi.ShellThemeOption{Key: k, Accent: sw.Accent, Accent2: sw.Accent2, Bg1: sw.Bg1})
	}
	return out
}

// clubLocation — CurrentClub::timezone(): неизвестный пояс — UTC.
func clubLocation(tz string) *time.Location { return clubtz.Location(tz) }

// settingsView — значения для формы (как их выводит Blade: число, @checked, @selected).
func settingsView(s clubSettings) settingsapi.ClubSettings {
	c, f, g := s[sectionClub], s[sectionFinances], s[sectionGuests]
	levels := func(r json.RawMessage, threshold string) []settingsapi.PercentLevel {
		_, list, err := encodeLevels(levelsOf(r, threshold))
		out := make([]settingsapi.PercentLevel, 0, len(list))
		if err != nil {
			return out
		}
		for _, l := range list {
			out = append(out, settingsapi.PercentLevel{ThresholdTiyn: l.ThresholdTiyn, PercentBp: l.PercentBP})
		}
		return out
	}
	return settingsapi.ClubSettings{
		Club: settingsapi.SettingsClubSection{
			SessionIdleTimeout:                    toFloat(c["session_idle_timeout"]),
			PcActionAfterSessionEnd:               phpString(c["pc_action_after_session_end"]),
			PcRestartAfterSessionEnd:              toFloat(c["pc_restart_after_session_end"]),
			SessionAutoTerminateWhenPcUnavailable: toFloat(c["session_auto_terminate_when_pc_unavailable"]),
			BookingInterval:                       toFloat(c["booking_interval"]),
			MaxCashiersOnShift:                    toFloat(c["max_cashiers_on_shift"]),
			MaxOperatorsOnShift:                   toFloat(c["max_operators_on_shift"]),
			AgentIdleLogoutMinutes:                toFloat(c["agent_idle_logout_minutes"]),
			AgentShopEnabled:                      truthy(c["agent_shop_enabled"]),
			AgentAutoUpdate:                       truthy(c["agent_auto_update"]),
			AgentShellTheme:                       shellThemeValue(c["agent_shell_theme"]),
			AppBookingEnabled:                     truthy(c["app_booking_enabled"]),
			AppBookingCancel:                      truthy(c["app_booking_cancel"]),
		},
		Finances: settingsapi.SettingsFinancesSection{
			Currency:                phpString(f["currency"]),
			LoyaltyEnabled:          truthy(f["loyalty_enabled"]),
			LoyaltyLevels:           levels(f["loyalty_levels"], bonus.ThresholdSpent),
			TopupBonusEnabled:       truthy(f["topup_bonus_enabled"]),
			TopupBonusLevels:        levels(f["topup_bonus_levels"], bonus.ThresholdAmount),
			DiscountScope:           phpString(f["discount_scope"]),
			BonusMaxPercent:         toFloat(f["bonus_max_percent"]),
			AllowMixedPayment:       truthy(f["allow_mixed_payment"]),
			AllowWholeBalance:       truthy(f["allow_whole_balance"]),
			AutoBilling:             truthy(f["auto_billing"]),
			AutoBillingPauseMinutes: toFloat(f["auto_billing_pause_minutes"]),
		},
		Guests: settingsapi.SettingsGuestsSection{
			ShowQrCodeOnLogin:             truthy(g["show_qr_code_on_login"]),
			AllowGuestSelfTransfer:        truthy(g["allow_guest_self_transfer"]),
			AllowAddingFriends:            truthy(g["allow_adding_friends"]),
			AllowBalanceTransferToFriends: truthy(g["allow_balance_transfer_to_friends"]),
			GuestModeEnabled:              truthy(g["guest_mode_enabled"]),
		},
	}
}

// SaveClubSettings — save(): поля секции из запроса, журнал settings.updated с диффом, аудит финансовых
// настроек, ConfigChanged агентам.
func (h *handlers) SaveClubSettings(ctx context.Context, req settingsapi.SaveClubSettingsRequestObject) (settingsapi.SaveClubSettingsResponseObject, error) {
	section := string(req.Section)
	if _, ok := sectionFields[section]; !ok {
		return nil, httpx.RuleError("section", cash.RuleIn)
	}
	body := map[string]any{}
	if req.Body != nil {
		body = *req.Body
	}
	in, err := readInput(body)
	if err != nil {
		return nil, httpx.NewAPIError(http.StatusBadRequest, "bad_request", "")
	}
	levels, fe := validate(section, in)
	if err := fe.Err(); err != nil {
		return nil, err
	}
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	err = h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		row, err := q.SettingsLockClub(ctx, p.ClubID)
		if err != nil {
			return err
		}
		s := loadSettings(row.Settings)
		changes, err := apply(s, section, in, levels)
		if err != nil {
			return err
		}
		doc, err := s.marshal(row.Settings)
		if err != nil {
			return err
		}
		if err := q.SettingsSave(ctx, store.SettingsSaveParams{ClubID: p.ClubID, Settings: doc, Now: &now}); err != nil {
			return err
		}
		if len(changes) == 0 {
			return nil
		}
		names := make([]string, len(changes))
		details := map[string]any{"section": section}
		before, after := map[string]json.RawMessage{}, map[string]json.RawMessage{}
		for i, c := range changes {
			names[i] = c.field
			details[c.field] = c.readable
			before[c.field], after[c.field] = c.before, c.after
		}
		if err := logClub(ctx, q, now, "settings.updated", details, logSettings, map[string]string{"fields": strings.Join(names, ", ")}); err != nil {
			return err
		}
		// Финансовые настройки (бонусы, скидки, оплата времени) влияют на деньги — аудит (CLAUDE.md «Безопасность»).
		if section == sectionFinances {
			return httpx.Audit(ctx, q, p.ClubID, "settings", "settings.updated", subjectClub, p.ClubID, before, after)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	h.configChanged(ctx, p.ClubID)
	return settingsapi.SaveClubSettings200JSONResponse{Message: h.d.T(ctx, msgSaved, nil)}, nil
}

// logClub — запись журнала действий о самом клубе (субъект — клуб) от сотрудника запроса: текст — ключ перевода
// key с подстановками params, details — подробности (nil — без них).
func logClub(ctx context.Context, q *store.Queries, now time.Time, event string, details map[string]any, key string, params map[string]string) error {
	p := httpx.Principal(ctx)
	_, err := activity.Record(ctx, q, activity.Entry{
		ClubID: p.ClubID, Event: event, UserID: p.UserID, SubjectType: subjectClub, SubjectID: p.ClubID, Details: details, At: now,
	}.Translated(key, params))
	return err
}

// configChanged — ConfigChanged агентам клуба после коммита (не блокирует ответ).
func (h *handlers) configChanged(ctx context.Context, clubID int64) {
	if h.d.ClubConfigChanged != nil {
		h.d.ClubConfigChanged(context.WithoutCancel(ctx), clubID)
	}
}

// SaveClubTimezone — timezone(): required|string|timezone:all; журнал, если пояс другой.
func (h *handlers) SaveClubTimezone(ctx context.Context, req settingsapi.SaveClubTimezoneRequestObject) (settingsapi.SaveClubTimezoneResponseObject, error) {
	tz := ""
	if req.Body != nil && req.Body.Timezone != nil {
		tz = strings.TrimSpace(*req.Body.Timezone)
	}
	switch {
	case tz == "":
		return nil, httpx.RuleError("timezone", cash.RuleRequired)
	case !validTimezone(tz):
		// Laravel: validation.timezone (в ru.php нет — английский текст по умолчанию).
		return nil, httpx.Validation("timezone", "The timezone field must be a valid timezone.")
	}
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	changed := false
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		row, err := q.SettingsLockClub(ctx, p.ClubID)
		if err != nil || row.Timezone == tz {
			return err
		}
		changed = true
		if err := q.SettingsSetTimezone(ctx, store.SettingsSetTimezoneParams{ClubID: p.ClubID, Timezone: tz, Now: &now}); err != nil {
			return err
		}
		return logClub(ctx, q, now, "club.timezone_changed", map[string]any{"from": row.Timezone, "to": tz}, logTimezone, map[string]string{"from": row.Timezone, "to": tz})
	})
	if err != nil {
		return nil, err
	}
	if changed {
		h.configChanged(ctx, p.ClubID)
	}
	return settingsapi.SaveClubTimezone200JSONResponse{Message: h.d.T(ctx, msgTimezoneSaved, nil)}, nil
}

// GetClubLanguages — languages(): языки платформы и язык клуба.
func (h *handlers) GetClubLanguages(ctx context.Context, _ settingsapi.GetClubLanguagesRequestObject) (settingsapi.GetClubLanguagesResponseObject, error) {
	p := httpx.Principal(ctx)
	out := settingsapi.LanguagesPage{Languages: []settingsapi.LanguageItem{}}
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		langs, err := q.SettingsLanguages(ctx)
		if err != nil {
			return err
		}
		row, err := q.SettingsClub(ctx, p.ClubID)
		if err != nil {
			return err
		}
		for _, l := range langs {
			out.Languages = append(out.Languages, settingsapi.LanguageItem{Code: l.Code, Name: l.Name, IsEnabled: l.IsEnabled})
		}
		out.Locale = forClub(row.Locale, langs)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return settingsapi.GetClubLanguages200JSONResponse(out), nil
}

// forClub — LocaleResolver::forClub(): язык клуба, если включён, иначе ru (если включён) или первый включённый.
func forClub(locale *string, langs []store.SettingsLanguagesRow) string {
	code := defaultLocale
	if locale != nil && *locale != "" {
		code = *locale
	}
	var enabled []string
	for _, l := range langs {
		if l.IsEnabled {
			enabled = append(enabled, l.Code)
		}
	}
	if len(enabled) == 0 {
		return code
	}
	for _, c := range []string{code, defaultLocale} {
		for _, e := range enabled {
			if e == c {
				return c
			}
		}
	}
	return enabled[0]
}

// SaveClubLanguage — LanguageController::select(): in:<включённые языки>; журнал club.language_changed;
// сообщение — уже на новом языке ($this->locales->apply()).
func (h *handlers) SaveClubLanguage(ctx context.Context, req settingsapi.SaveClubLanguageRequestObject) (settingsapi.SaveClubLanguageResponseObject, error) {
	locale := ""
	if req.Body != nil && req.Body.Locale != nil {
		locale = strings.TrimSpace(*req.Body.Locale)
	}
	if locale == "" {
		return nil, httpx.RuleError("locale", cash.RuleRequired)
	}
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		langs, err := q.SettingsLanguages(ctx)
		if err != nil {
			return err
		}
		ok := false
		for _, l := range langs {
			ok = ok || (l.IsEnabled && l.Code == locale)
		}
		if !ok {
			return httpx.RuleError("locale", cash.RuleIn)
		}
		row, err := q.SettingsLockClub(ctx, p.ClubID)
		if err != nil {
			return err
		}
		from := forClub(row.Locale, langs)
		if err := q.SettingsSetLocale(ctx, store.SettingsSetLocaleParams{ClubID: p.ClubID, Locale: &locale, Now: &now}); err != nil {
			return err
		}
		return logClub(ctx, q, now, "club.language_changed", map[string]any{"from": from, "to": locale}, logLanguage, map[string]string{"from": from, "to": locale})
	})
	if err != nil {
		return nil, err
	}
	h.d.I18n.SetClubLocale(p.ClubID, locale) // этот инстанс — сразу; остальные — фоновой сверкой каталога
	h.configChanged(ctx, p.ClubID)
	msg := msgLanguageSaved
	if h.d.I18n != nil {
		msg = h.d.I18n.Translate(ctx, locale, msgLanguageSaved, nil)
	}
	return settingsapi.SaveClubLanguage200JSONResponse{Message: msg}, nil
}

var pinRe = regexp.MustCompile(`^\d{4,8}$`)

// SaveAgentPin — agentPin(): nullable|string|regex:/^\d{4,8}$/|confirmed; пустой PIN удаляет его. Хэш —
// Argon2id (решение Q-S6: старые bcrypt-хэши проверяются и перехэшируются при входе).
func (h *handlers) SaveAgentPin(ctx context.Context, req settingsapi.SaveAgentPinRequestObject) (settingsapi.SaveAgentPinResponseObject, error) {
	var pin, confirmation *string
	if req.Body != nil {
		pin, confirmation = emptyToNil(req.Body.Pin), emptyToNil(req.Body.PinConfirmation)
	}
	if pin != nil {
		var fe httpx.FormErrors
		if !pinRe.MatchString(*pin) {
			fe.Add("pin", msgPinFormat)
		}
		if confirmation == nil || *confirmation != *pin {
			fe.Rule("pin", "confirmed", "")
		}
		if err := fe.Err(); err != nil {
			return nil, err
		}
	}
	if h.d.Passwords == nil && pin != nil {
		return nil, httpx.ErrNotConfigured
	}
	var hash *string
	if pin != nil {
		hv, err := h.d.Passwords.Hash(ctx, *pin)
		if err != nil {
			return nil, err
		}
		hash = &hv
	}
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		if _, err := q.SettingsLockClub(ctx, p.ClubID); err != nil {
			return err
		}
		if err := q.SettingsSetAgentPin(ctx, store.SettingsSetAgentPinParams{ClubID: p.ClubID, PinHash: hash, Now: &now}); err != nil {
			return err
		}
		key := logPinSet
		if hash == nil {
			key = logPinDelete
		}
		return logClub(ctx, q, now, "club.agent_pin_changed", nil, key, nil)
	})
	if err != nil {
		return nil, err
	}
	h.configChanged(ctx, p.ClubID)
	msg := msgPinSaved
	if hash == nil {
		msg = msgPinDeleted
	}
	return settingsapi.SaveAgentPin200JSONResponse{Message: h.d.T(ctx, msg, nil)}, nil
}

func emptyToNil(s *string) *string {
	if s == nil || strings.TrimSpace(*s) == "" {
		return nil
	}
	v := strings.TrimSpace(*s)
	return &v
}

// SaveAgentStaffTime — agentStaffTime(): required|integer|min:1|max:240; журнал, если значение другое.
func (h *handlers) SaveAgentStaffTime(ctx context.Context, req settingsapi.SaveAgentStaffTimeRequestObject) (settingsapi.SaveAgentStaffTimeResponseObject, error) {
	v := raw("null")
	if req.Body != nil && req.Body.Minutes != nil {
		b, err := json.Marshal(req.Body.Minutes)
		if err != nil {
			return nil, httpx.NewAPIError(http.StatusBadRequest, "bad_request", "")
		}
		v = b
		if jsonKind(v) == 's' && strings.TrimSpace(stringOf(v)) == "" {
			v = raw("null")
		}
	}
	var fe httpx.FormErrors
	validateNumber(&fe, "minutes", v, true, numberRule{required: true, integer: true, min: "1", max: "240"})
	if err := fe.Err(); err != nil {
		return nil, err
	}
	minutes := int32(phpIntCast(v)) // #nosec G115 -- 1…240 после проверки
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	changed := false
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		row, err := q.SettingsLockClub(ctx, p.ClubID)
		if err != nil || row.AgentStaffMinutes == minutes {
			return err
		}
		changed = true
		if err := q.SettingsSetStaffMinutes(ctx, store.SettingsSetStaffMinutesParams{ClubID: p.ClubID, Minutes: minutes, Now: &now}); err != nil {
			return err
		}
		from, to := strconv.Itoa(int(row.AgentStaffMinutes)), strconv.Itoa(int(minutes))
		return logClub(ctx, q, now, "club.agent_staff_time_changed", map[string]any{"from": row.AgentStaffMinutes, "to": minutes}, logStaffTime, map[string]string{"from": from, "to": to})
	})
	if err != nil {
		return nil, err
	}
	if changed {
		h.configChanged(ctx, p.ClubID)
	}
	return settingsapi.SaveAgentStaffTime200JSONResponse{Message: h.d.T(ctx, msgStaffTimeSaved, nil)}, nil
}
