package legacy

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"time"

	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// phpFloat — число, как его отдаёт json_encode((float) $x) в PHP ≥ 7.1 (serialize_precision = -1): кратчайшая
// запись, целое — с ".0" (1500.0). Так старый API отдавал балансы и скидку в /api/auth/*.
type phpFloat float64

// MarshalJSON — 1500.5, 1500.0, 0.0.
func (f phpFloat) MarshalJSON() ([]byte, error) {
	s := strconv.FormatFloat(float64(f), 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return []byte(s), nil
}

func phpFloatOf(f float64) phpFloat { return phpFloat(f) }

// tenge — (float) decimal(12,2) тенге из тиынов: деление точное до ближайшего double, как разбор "1500.50".
func tenge(t int64) phpFloat { return phpFloat(float64(t) / 100) }

// moneyStr — StaffApiController::money(): number_format((float) $x, 2, '.', ”) — "1500.00", "-150.00".
func moneyStr(t int64) string { return money.Tiyn(t).Tenge() }

// moneySpaced — number_format($x, 2, '.', ' ') ("1 500.00"), сообщения о бонусе.
func moneySpaced(t int64) string { return money.NumberFormat(money.Tiyn(t), ".", " ") }

// moneyComma — number_format($x, 2, ',', ' ') ("1 500,00"), сообщение гостевой сессии.
func moneyComma(t int64) string { return money.NumberFormat(money.Tiyn(t), ",", " ") }

// zulu — StaffApiController::at(): toIso8601ZuluString() ("2026-10-31T12:00:00Z"); nil — null.
func zulu(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format("2006-01-02T15:04:05Z")
	return &s
}

// iso — Carbon::toIso8601String() в UTC ("2026-10-31T12:00:00+00:00"); nil — null.
func iso(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format("2006-01-02T15:04:05-07:00")
	return &s
}

// clubSetting — нужное из clubs.settings (data_get старого кода).
type clubSetting struct {
	raw map[string]map[string]any
}

func parseClubSetting(raw []byte) clubSetting {
	var cs clubSetting
	_ = json.Unmarshal(raw, &cs.raw)
	return cs
}

func (cs clubSetting) value(section, key string) any {
	if cs.raw == nil {
		return nil
	}
	return cs.raw[section][key]
}

// currency — data_get($settings, 'finances.currency', 'KZT').
// Старый мобильный API отдаёт значение буквально (пустая строка, не-строка — как есть): billingsvc.CurrencyOf
// в режиме literal; новый сервер в остальных местах берёт только непустую строку (BC-06).
func (cs clubSetting) currency() string {
	return billingsvc.CurrencyOf(cs.value("finances", "currency"), true)
}

// flag — filter_var($v ?? $def, FILTER_VALIDATE_BOOLEAN).
func (cs clubSetting) flag(section, key string, def bool) bool {
	switch v := cs.value(section, key).(type) {
	case nil:
		return def
	case bool:
		return v
	case float64:
		return v == 1
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "on", "yes":
			return true
		}
	}
	return false
}

// guestMode — GuestFeatures::guestModeEnabled() (guests.guest_mode_enabled, по умолчанию включён).
func (cs clubSetting) guestMode() bool { return cs.flag("guests", "guest_mode_enabled", true) }

// GuestFeatures::SETTINGS: функция → настройка раздела «Гости» (по умолчанию выключены).
var guestFeatureSettings = []struct{ name, setting string }{
	{"qr_login", "show_qr_code_on_login"},
	{"self_transfer", "allow_guest_self_transfer"},
	{"friends", "allow_adding_friends"},
	{"balance_transfer", "allow_balance_transfer_to_friends"},
}

// features — GuestFeatures::all(): {qr_login, self_transfer, friends, balance_transfer}.
func (cs clubSetting) features() map[string]bool {
	out := make(map[string]bool, len(guestFeatureSettings))
	for _, f := range guestFeatureSettings {
		out[f.name] = cs.flag("guests", f.setting, false)
	}
	return out
}

func nopBody(raw []byte) io.ReadCloser { return io.NopCloser(bytes.NewReader(raw)) }

// tiynOf — сумма из JSON приложения (число или строка тенге) → тиыны точно; ok=false — не число.
func tiynOf(s string) (money.Tiyn, bool) {
	t, err := money.ParseTengeRounded(strings.TrimSpace(s))
	return t, err == nil
}
