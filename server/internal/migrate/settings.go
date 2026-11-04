package migrate

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
)

// Поля clubs.settings по секциям — как SettingsController::save() ($fields) старого проекта и экран настроек
// нового (internal/http/sections/settings/values.go: sectionFields, booleanFields, minuteFields).
var (
	settingsFields = map[string][]string{
		"club": {
			"session_idle_timeout", "pc_action_after_session_end", "pc_restart_after_session_end",
			"session_auto_terminate_when_pc_unavailable", "booking_interval", "max_cashiers_on_shift",
			"max_operators_on_shift", "agent_idle_logout_minutes", "agent_shop_enabled", "agent_auto_update",
		},
		"finances": {
			"currency", "loyalty_enabled", "loyalty_levels", "topup_bonus_enabled", "topup_bonus_levels", "discount_scope",
			"bonus_max_percent", "allow_mixed_payment", "allow_whole_balance", "auto_billing", "auto_billing_pause_minutes",
		},
		"guests": {
			"show_qr_code_on_login", "allow_guest_self_transfer", "allow_adding_friends",
			"allow_balance_transfer_to_friends", "guest_mode_enabled",
		},
	}
	settingsBooleans = []string{
		"loyalty_enabled", "topup_bonus_enabled", "allow_mixed_payment", "allow_whole_balance", "auto_billing",
		"show_qr_code_on_login", "allow_guest_self_transfer", "allow_adding_friends", "allow_balance_transfer_to_friends",
		"agent_shop_enabled", "agent_auto_update", "guest_mode_enabled",
	}
	settingsMinutes = []string{
		"session_idle_timeout", "pc_restart_after_session_end", "session_auto_terminate_when_pc_unavailable",
		"booking_interval", "max_cashiers_on_shift", "max_operators_on_shift", "agent_idle_logout_minutes",
	}
)

// legacySlug — клуб, созданный бэкфиллом старого проекта (2026_10_13_040139_backfill_club_data): его настройки
// жили в storage/app/settings.json (CurrentClub::settings(), ~71–83), а clubs.settings мог остаться NULL (Q-022).
const legacySlug = "legacy"

// checkClubSettings — Q-022 (клуб legacy без settings: файл или дефолты) и Q-023 (значения вне формата,
// неизвестные ключи — в отчёт). Сами настройки переносятся как есть (формат v1): нормализация и уровни v2 — M2;
// новый сервер читает старые значения так же, как SettingsController::minutes() (values.go loadSettings()).
func (r *run) checkClubSettings(row []*string, idx map[string]int, set func(string, string)) error {
	raw := row[idx["settings"]]
	if raw == nil {
		if r.club.Slug != legacySlug {
			return nil
		}
		if r.o.LegacySettingsFile == nil {
			r.rep.add(Warning, CodeSettingsLegacy, "clubs", "settings", "", strconv.FormatInt(r.club.ID, 10),
				"клуб legacy без settings и без файла storage/app/settings.json — будут настройки по умолчанию")
			return nil
		}
		if !json.Valid(r.o.LegacySettingsFile) {
			r.rep.add(Blocker, CodeSettingsLegacy, "clubs", "settings", "", strconv.FormatInt(r.club.ID, 10),
				"файл настроек клуба legacy — не JSON")
			return nil
		}
		set("settings", string(r.o.LegacySettingsFile))
		r.rep.add(Warning, CodeSettingsLegacy, "clubs", "settings", "", strconv.FormatInt(r.club.ID, 10),
			"клуб legacy: settings взяты из файла storage/app/settings.json")
		raw = row[idx["settings"]]
	}
	r.checkSettingsJSON([]byte(*raw))
	return nil
}

func (r *run) checkSettingsJSON(stored []byte) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(stored, &top); err != nil {
		r.rep.add(Warning, CodeSettingsFormat, "clubs", "settings", "", "", "settings — не JSON-объект (новый сервер возьмёт дефолты)")
		return
	}
	for section, body := range top {
		known, ok := settingsFields[section]
		if !ok {
			r.rep.add(Warning, CodeSettingsFormat, "clubs", "settings", section, "", "неизвестная секция настроек (сохраняется как есть)")
			continue
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			r.rep.add(Warning, CodeSettingsFormat, "clubs", "settings", section, "", "секция настроек — не объект")
			continue
		}
		for key, v := range fields {
			path := section + "." + key
			if threshold, ok := levelThresholds[key]; ok && section == "finances" {
				r.checkLevels(path, threshold, v)
				continue
			}
			switch {
			case !slices.Contains(known, key):
				r.rep.add(Warning, CodeSettingsFormat, "clubs", "settings", path, "", "неизвестный (черновой) ключ настроек")
			case slices.Contains(settingsMinutes, key) && !isJSONNumber(v):
				r.rep.add(Warning, CodeSettingsFormat, "clubs", "settings", path+"="+string(v), "",
					"минуты строкой вместо числа (читается как SettingsController::minutes())")
			case slices.Contains(settingsBooleans, key) && !isJSONBool(v):
				r.rep.add(Warning, CodeSettingsFormat, "clubs", "settings", path+"="+string(v), "", "флаг не boolean")
			}
		}
	}
}

// levelThresholds — уровни лояльности и бонуса за пополнение: поле → ключ порога (bonus.ThresholdSpent/Amount).
var levelThresholds = map[string]string{"loyalty_levels": "spent", "topup_bonus_levels": "amount"}

// checkLevels — BQ-3: уровни должны быть списком без равных порогов (jsonb не хранит порядок ключей объекта и
// повторы, а PercentLevels::normalize() при равных порогах опирался на исходный порядок).
func (r *run) checkLevels(path, threshold string, v json.RawMessage) {
	t := bytes.TrimSpace(v)
	if len(t) == 0 || string(t) == "null" {
		return
	}
	var levels []map[string]json.RawMessage
	if err := json.Unmarshal(t, &levels); err != nil {
		r.rep.add(Warning, CodeSettingsFormat, "clubs", "settings", path, "",
			"уровни не списком объектов (BQ-3: порядок/повторы в jsonb не сохраняются)")
		return
	}
	seen := map[string]bool{}
	for _, l := range levels {
		key := string(bytes.TrimSpace(l[threshold]))
		if f, err := strconv.ParseFloat(strings.Trim(key, `"`), 64); err == nil {
			key = strconv.FormatFloat(f, 'f', -1, 64)
		}
		if seen[key] {
			r.rep.add(Warning, CodeSettingsFormat, "clubs", "settings", path+"."+threshold+"="+key, "",
				"равные пороги уровней (BQ-3: победитель зависел от порядка)")
		}
		seen[key] = true
	}
}

func isJSONNumber(v json.RawMessage) bool {
	t := bytes.TrimSpace(v)
	return len(t) > 0 && (t[0] == '-' || (t[0] >= '0' && t[0] <= '9'))
}

func isJSONBool(v json.RawMessage) bool {
	t := string(bytes.TrimSpace(v))
	return t == "true" || t == "false"
}
