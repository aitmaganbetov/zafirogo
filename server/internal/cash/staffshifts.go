package cash

import (
	"encoding/json"
	"strings"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// Роли с ограничением «на смене». Laravel: app/Support/ClubRoles.php SHIFT = ['cashier', 'operator'].
const (
	RoleCashier  = "cashier"
	RoleOperator = "operator"
)

// ShiftLimitSetting — ключ настройки club.* с лимитом для роли.
// Laravel: app/Services/StaffShifts.php SETTINGS.
func ShiftLimitSetting(role string) string {
	switch role {
	case RoleCashier:
		return "max_cashiers_on_shift"
	case RoleOperator:
		return "max_operators_on_shift"
	}
	return ""
}

// ShiftLimit — лимит сотрудников роли на смене; nil — без лимита (BR-060).
//
// Laravel: StaffShifts::limit() (~стр. 18–23):
//
//	$value = $clubSettings['club'][SETTINGS[$role] ?? ''] ?? 0;
//	return is_numeric($value) && (int) $value > 0 ? (int) $value : null;
//
// value — значение настройки из JSON (float64, string, json.Number, bool, nil…). bool и прочее — не is_numeric.
func ShiftLimit(value any) *int64 {
	var n int64
	switch v := value.(type) {
	case float64:
		n = money.PHPIntCast(v)
	case int:
		n = int64(v)
	case int64:
		n = v
	case json.Number:
		if !money.PHPIsNumeric(string(v)) {
			return nil
		}
		n = money.PHPStringToInt(string(v))
	case string:
		if !money.PHPIsNumeric(v) {
			return nil
		}
		// (int) "строка" в PHP 8: ведущее число; пробелы в конце допустимы для is_numeric.
		n = money.PHPStringToInt(strings.TrimLeft(v, " \t\n\r\v\f"))
	default:
		return nil
	}
	if n <= 0 {
		return nil
	}
	return &n
}

// HasShiftPlace — может ли сотрудник войти в веб-панель: роль без лимита, лимита нет, или на смене
// (с живой сессией панели, кроме него самого) меньше лимита (BR-060).
// Laravel: StaffShifts::hasPlace() (~стр. 48–57). Кто «на смене» — onShift() по таблице web-сессий; в Go — по активным
// сессиям панели (OQ-BR-09), это считает вызывающий: onShiftOthers.
func HasShiftPlace(role string, limit *int64, onShiftOthers int) bool {
	if role != RoleCashier && role != RoleOperator {
		return true
	}
	return limit == nil || int64(onShiftOthers) < *limit
}
