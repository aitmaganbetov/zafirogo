package bonus

import "errors"

// Settings — ключи `finances.*` из JSON `clubs.settings`, которые читают TopUpBonus и LoyaltyProgram,
// с теми же значениями по умолчанию, что в старом коде (BR-003): сервисы читают СЫРОЙ JSON
// (CurrentClub::settings()), а не слитый с SettingsController::defaultSettings().
//
// Решение Q-003 (docs/DECISIONS.md): уровни читаются в обоих форматах — v2 ({"v": 2, "levels": [{threshold_tiyn,
// percent_bp}]}, EncodeLevels) и формат старого кода (список в тенге и процентах, Normalize).
// Ограничения из SaveSettingsRequest (≤ 20 уровней, 0.01…100 % и т.п.) на чтении НЕ применяются —
// старый код их тоже не применял (см. ValidateLevels для сохранения).
type Settings struct {
	// finances.topup_bonus_enabled: (bool) data_get(..., false).
	// Laravel: app/Services/TopUpBonus.php TopUpBonus::enabled() (~стр. 17–20).
	TopUpBonusEnabled bool
	// finances.topup_bonus_levels: TopUpBonus::normalize(data_get(..., [])) — уже нормализованы.
	// Laravel: app/Services/TopUpBonus.php TopUpBonus::levels() (~стр. 23–26).
	TopUpBonusLevels []Level
	// finances.loyalty_enabled: (bool) data_get(..., false).
	// Laravel: app/Services/LoyaltyProgram.php LoyaltyProgram::enabled() (~стр. 21–24).
	LoyaltyEnabled bool
	// finances.loyalty_levels: LoyaltyProgram::normalize(data_get(..., [])).
	// Laravel: app/Services/LoyaltyProgram.php LoyaltyProgram::levels() (~стр. 27–30).
	LoyaltyLevels []Level
}

// TopUpLevelsInForce — TopUpBonus::levels(): уровни, если бонус включён, иначе [].
func (s Settings) TopUpLevelsInForce() []Level {
	if !s.TopUpBonusEnabled {
		return []Level{}
	}
	return s.TopUpBonusLevels
}

// LoyaltyLevelsInForce — LoyaltyProgram::levels(): уровни, если накопительная скидка включена, иначе [].
func (s Settings) LoyaltyLevelsInForce() []Level {
	if !s.LoyaltyEnabled {
		return []Level{}
	}
	return s.LoyaltyLevels
}

// ErrSettingsNotArray — clubs.settings декодируется не в массив (например, `5` или `"x"`).
// В PHP CurrentClub::settings(): array вернул бы TypeError — клуб не работал бы.
var ErrSettingsNotArray = errors.New("bonus: clubs.settings — не объект JSON")

// ParseSettings читает бонусные ключи из сырого JSON `clubs.settings`.
// Пустой вход, `null` и некорректный JSON — все значения по умолчанию (Eloquent-каст 'array'
// даёт null, CurrentClub::settings() → []). Особый случай клуба `legacy` с settings.json — не здесь (OQ-BR-03).
func ParseSettings(raw []byte) (Settings, error) {
	if len(raw) == 0 {
		return SettingsFrom(Null())
	}
	v, err := DecodeJSON(raw)
	if err != nil {
		return SettingsFrom(Null())
	}
	return SettingsFrom(v)
}

// SettingsFrom — то же для уже декодированного значения settings.
func SettingsFrom(settings Value) (Settings, error) {
	if settings.Kind() == KindNull {
		settings = List()
	}
	if !settings.IsArray() {
		return Settings{}, ErrSettingsNotArray
	}
	return Settings{
		TopUpBonusEnabled: dataGet(settings, "finances", "topup_bonus_enabled", Bool(false)).Truthy(),
		TopUpBonusLevels:  levelsFrom(dataGet(settings, "finances", "topup_bonus_levels", List()), ThresholdAmount),
		LoyaltyEnabled:    dataGet(settings, "finances", "loyalty_enabled", Bool(false)).Truthy(),
		LoyaltyLevels:     levelsFrom(dataGet(settings, "finances", "loyalty_levels", List()), ThresholdSpent),
	}, nil
}

// dataGet — data_get($settings, "$section.$key", $default) Laravel для массивов: на каждом шаге
// цель должна быть массивом с этим ключом (Arr::exists), иначе $default; существующий ключ
// со значением null возвращает null (не $default).
func dataGet(settings Value, section, key string, def Value) Value {
	for _, k := range []string{section, key} {
		if !settings.IsArray() {
			return def
		}
		if _, ok := settings.arr.vals[phpKey(k)]; !ok {
			return def
		}
		settings = settings.Get(k)
	}
	return settings
}
