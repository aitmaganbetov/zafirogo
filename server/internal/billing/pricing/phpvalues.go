package pricing

import (
	"bytes"
	"encoding/json"
	"strconv"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// Значения настроек клуба хранятся в JSON (clubs.settings) и читаются старым кодом «сырыми»
// (CurrentClub::settings(), BR-003): json_decode($json, true), затем is_numeric / (int) / (bool).
// Ниже — эти приведения PHP 8.4 для значений, которые даёт encoding/json (лучше с UseNumber).

// phpIsNumeric — is_numeric($v): int и float — да; строка — числовая строка PHP (money.PHPIsNumeric);
// null, bool, массивы — нет.
func phpIsNumeric(v any) bool {
	switch x := v.(type) {
	case json.Number, float64, float32, int, int64, int32:
		return true
	case string:
		return money.PHPIsNumeric(x)
	default:
		return false
	}
}

// phpToInt — (int)$v для числового значения: int как есть; float — zend_dval_to_lval (money.PHPIntCast);
// строка — money.PHPStringToInt. JSON-число без точки и экспоненты json_decode отдаёт int, а если оно
// не помещается в int64 — float (без JSON_BIGINT_AS_STRING), так же и здесь.
func phpToInt(v any) int64 {
	switch x := v.(type) {
	case json.Number:
		if i, err := strconv.ParseInt(string(x), 10, 64); err == nil {
			return i
		}
		f, _ := strconv.ParseFloat(string(x), 64) // ±Inf для 1e400 — PHPIntCast даёт 0, как PHP
		return money.PHPIntCast(f)
	case float64:
		return money.PHPIntCast(x)
	case float32:
		return money.PHPIntCast(float64(x))
	case int:
		return int64(x)
	case int64:
		return x
	case int32:
		return int64(x)
	case string:
		return money.PHPStringToInt(x)
	case bool:
		if x {
			return 1
		}
		return 0
	default:
		return 0
	}
}

// phpToBool — (bool)$v: false для null, false, 0, 0.0, "", "0" и пустого массива; иначе true.
// Внимание: строка "false" — true (как в PHP).
func phpToBool(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case json.Number:
		if i, err := strconv.ParseInt(string(x), 10, 64); err == nil {
			return i != 0
		}
		f, _ := strconv.ParseFloat(string(x), 64)
		return f != 0
	case float64:
		return x != 0
	case float32:
		return x != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	case int32:
		return x != 0
	case string:
		return x != "" && x != "0"
	case map[string]any:
		return len(x) > 0
	case []any:
		return len(x) > 0
	default:
		return true
	}
}

// RawFinances — сырой раздел finances настроек клуба (clubs.settings->finances), как его видит
// data_get($settings, 'finances.*'). nil — раздела нет (все значения по умолчанию).
type RawFinances map[string]any

// ParseFinances разбирает JSON всего clubs.settings и возвращает раздел finances. Числа читаются как
// json.Number (int/float — как json_decode). Если settings или finances не объект — пустой раздел
// (data_get вернёт умолчания). Пустой ввод и JSON null — тоже пустой раздел.
func ParseFinances(settingsJSON []byte) (RawFinances, error) {
	if len(bytes.TrimSpace(settingsJSON)) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(settingsJSON))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return nil, err
	}
	obj, ok := root.(map[string]any)
	if !ok {
		return nil, nil
	}
	fin, ok := obj["finances"].(map[string]any)
	if !ok {
		return nil, nil
	}
	return RawFinances(fin), nil
}
