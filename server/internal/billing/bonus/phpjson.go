package bonus

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// Kind — тип значения PHP после json_decode($json, true).
type Kind uint8

// Типы значений PHP.
const (
	KindNull Kind = iota
	KindBool
	KindInt
	KindFloat
	KindString
	KindArray // и JSON-массив, и JSON-объект: в PHP оба — упорядоченный array
)

// Value — значение из JSON `clubs.settings` так, как его видел старый код после каста
// Eloquent 'array' (json_decode($value, true)): целые литералы — int (вне int64 — float),
// литералы с точкой/экспонентой — float (1e400 → INF), объекты и массивы — упорядоченный
// PHP array с сохранением порядка ключей. Порядок важен: сортировка уровней в
// PercentLevels::normalize() стабильная, и при равных порогах побеждает исходный порядок.
type Value struct {
	kind Kind
	b    bool
	i    int64
	f    float64
	s    string
	arr  *phpArray
}

// phpArray — упорядоченный массив PHP. Ключи нормализованы как в PHP: строка из канонического
// десятичного целого ("0", "-5") становится int-ключом, повторный ключ заменяет значение на
// прежнем месте (json_decode: последний выигрывает, позиция — первого).
type phpArray struct {
	keys []string
	vals map[string]Value
}

// Конструкторы значений (для тестов и вызывающего кода).

// Null — PHP null.
func Null() Value { return Value{} }

// Bool — PHP bool.
func Bool(b bool) Value { return Value{kind: KindBool, b: b} }

// Int — PHP int.
func Int(i int64) Value { return Value{kind: KindInt, i: i} }

// Float — PHP float.
func Float(f float64) Value { return Value{kind: KindFloat, f: f} }

// String — PHP string.
func String(s string) Value { return Value{kind: KindString, s: s} }

// List — PHP list (ключи 0…n-1).
func List(items ...Value) Value {
	a := &phpArray{vals: make(map[string]Value, len(items))}
	for i, v := range items {
		a.set(intKey(int64(i)), v)
	}
	return Value{kind: KindArray, arr: a}
}

// Object — PHP ассоциативный массив; pairs — ключ, значение, ключ, значение…
// Ключи нормализуются как в PHP (см. phpArray).
func Object(pairs ...any) Value {
	a := &phpArray{vals: make(map[string]Value, len(pairs)/2)}
	for i := 0; i+1 < len(pairs); i += 2 {
		k, _ := pairs[i].(string)
		v, _ := pairs[i+1].(Value)
		a.set(phpKey(k), v)
	}
	return Value{kind: KindArray, arr: a}
}

// Kind — тип значения.
func (v Value) Kind() Kind { return v.kind }

// IsArray — is_array($v).
func (v Value) IsArray() bool { return v.kind == KindArray }

// Get — $v[$key] ?? null для строкового ключа (как PHP: "0" ищется как int-ключ 0).
// Не массив или нет ключа — null.
func (v Value) Get(key string) Value {
	if v.kind != KindArray {
		return Value{}
	}
	return v.arr.vals[phpKey(key)]
}

// Len — count($v) для массива, иначе 0.
func (v Value) Len() int {
	if v.kind != KindArray {
		return 0
	}
	return len(v.arr.keys)
}

// Items — значения массива в порядке ключей (array_values).
func (v Value) Items() []Value {
	if v.kind != KindArray {
		return nil
	}
	out := make([]Value, len(v.arr.keys))
	for i, k := range v.arr.keys {
		out[i] = v.arr.vals[k]
	}
	return out
}

// Keys — ключи массива в порядке PHP; int-ключи — десятичной строкой.
func (v Value) Keys() []string {
	if v.kind != KindArray {
		return nil
	}
	out := make([]string, len(v.arr.keys))
	for i, k := range v.arr.keys {
		out[i] = k[2:]
	}
	return out
}

func (a *phpArray) set(k string, v Value) {
	if _, ok := a.vals[k]; !ok {
		a.keys = append(a.keys, k)
	}
	a.vals[k] = v
}

func intKey(i int64) string { return "i:" + strconv.FormatInt(i, 10) }

// phpKey — ZEND_HANDLE_NUMERIC_STR: строка вида "0", "123", "-5" (без ведущих нулей, без "+",
// не "-0", в пределах int64) — int-ключ, иначе строковый.
func phpKey(s string) string {
	if isCanonicalInt(s) {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return intKey(n)
		}
	}
	return "s:" + s
}

func isCanonicalInt(s string) bool {
	d := strings.TrimPrefix(s, "-")
	if d == "" || len(d) > 20 || (d[0] == '0' && (len(d) > 1 || len(s) != len(d))) {
		return false
	}
	for i := 0; i < len(d); i++ {
		if d[i] < '0' || d[i] > '9' {
			return false
		}
	}
	return true
}

// Truthy — (bool) $v по правилам PHP: false, 0, 0.0, -0.0, "", "0", null и пустой массив — false.
// Строка "false" — true (так старый код читал, например, finances.loyalty_enabled).
func (v Value) Truthy() bool {
	switch v.kind {
	case KindBool:
		return v.b
	case KindInt:
		return v.i != 0
	case KindFloat:
		return v.f != 0 // NaN != 0 → true, как в PHP
	case KindString:
		return v.s != "" && v.s != "0"
	case KindArray:
		return len(v.arr.keys) > 0
	default:
		return false
	}
}

// IsNumeric — is_numeric($v): int и float — да, строка — по правилам PHP 8, прочее — нет.
func (v Value) IsNumeric() bool {
	switch v.kind {
	case KindInt, KindFloat:
		return true
	case KindString:
		return money.PHPIsNumeric(v.s)
	default:
		return false
	}
}

// ToFloat — (float) $v для скаляров (массивы в старом коде сюда не доходят: их отсекает is_numeric).
func (v Value) ToFloat() float64 {
	switch v.kind {
	case KindBool:
		if v.b {
			return 1
		}
		return 0
	case KindInt:
		return float64(v.i)
	case KindFloat:
		return v.f
	case KindString:
		return money.PHPStringToFloat(v.s)
	case KindArray:
		if len(v.arr.keys) > 0 {
			return 1
		}
		return 0
	default:
		return 0
	}
}

// ErrNotJSON — вход не является корректным JSON (json_decode вернул бы null).
var ErrNotJSON = errors.New("bonus: некорректный JSON")

// DecodeJSON — json_decode($data, true) с сохранением порядка ключей и типов int/float, как в PHP.
// Некорректный JSON → ErrNotJSON (PHP вернул бы null).
func DecodeJSON(data []byte) (Value, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return Value{}, ErrNotJSON
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Value{}, ErrNotJSON
	}
	return v, nil
}

func decodeValue(dec *json.Decoder) (Value, error) {
	tok, err := dec.Token()
	if err != nil {
		return Value{}, err
	}
	switch t := tok.(type) {
	case nil:
		return Value{}, nil
	case bool:
		return Bool(t), nil
	case string:
		return String(t), nil
	case json.Number:
		return decodeNumber(string(t)), nil
	case json.Delim:
		a := &phpArray{vals: map[string]Value{}}
		switch t {
		case '[':
			for i := int64(0); dec.More(); i++ {
				v, err := decodeValue(dec)
				if err != nil {
					return Value{}, err
				}
				a.set(intKey(i), v)
			}
		case '{':
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return Value{}, err
				}
				k, _ := kt.(string)
				v, err := decodeValue(dec)
				if err != nil {
					return Value{}, err
				}
				a.set(phpKey(k), v)
			}
		}
		if _, err := dec.Token(); err != nil { // закрывающая скобка
			return Value{}, err
		}
		return Value{kind: KindArray, arr: a}, nil
	}
	return Value{}, ErrNotJSON
}

// decodeNumber — сканер ext/json: целый литерал — int (переполнение → float через zend_strtod),
// с точкой или экспонентой — float (переполнение → ±INF).
func decodeNumber(s string) Value {
	if !strings.ContainsAny(s, ".eE") {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return Int(n)
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil && !math.IsInf(f, 0) {
		return Float(0)
	}
	return Float(f)
}
