package webhook

// Суммы в теле вебхука. Решение RQ-3 (docs/DECISIONS.md): суммы — строки с двумя знаками ("150.00", как
// number_format($x, 2, '.', '') в старом WebhookDispatcher) плюс целые поля *_tiyn; JSON-чисел с дробной частью
// (float) в вебхуке нет. Сервисы пишут в outbox и форматы старого журнала (PHPFloat у строк чека sale.completed),
// поэтому формат приводится здесь — при сборке тела вебхука, а не в каждом сервисе.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// moneyKeys — поля-суммы (тенге) в data вебхуков: sale.completed.amount, items[].price/total, product.*.price.
var moneyKeys = []string{"amount", "price", "total"}

// tiynSuffix — суффикс поля с той же суммой в тиынах.
const tiynSuffix = "_tiyn"

// errBadPayload — данные события нельзя привести к формату вебхука (повтор не поможет).
var errBadPayload = errors.New("webhook: bad event data")

// normalizeMoney приводит суммы в data к виду RQ-3. Если менять нечего, возвращает data как есть
// (порядок ключей и байты не трогаются).
func normalizeMoney(data json.RawMessage) (json.RawMessage, error) {
	if len(data) == 0 {
		return data, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("%w: %w", errBadPayload, err)
	}
	changed, err := walkMoney(v)
	if err != nil {
		return nil, err
	}
	if !changed {
		return data, nil
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errBadPayload, err)
	}
	return out, nil
}

func walkMoney(v any) (bool, error) {
	changed := false
	switch x := v.(type) {
	case []any:
		for _, item := range x {
			c, err := walkMoney(item)
			if err != nil {
				return false, err
			}
			changed = changed || c
		}
	case map[string]any:
		for _, k := range moneyKeys {
			c, err := fixAmount(x, k)
			if err != nil {
				return false, err
			}
			changed = changed || c
		}
		for _, item := range x {
			c, err := walkMoney(item)
			if err != nil {
				return false, err
			}
			changed = changed || c
		}
	}
	return changed, nil
}

// fixAmount — m[key] строкой "N.NN" и m[key+"_tiyn"] целым. Источник правды — *_tiyn, если он есть.
func fixAmount(m map[string]any, key string) (bool, error) {
	raw, ok := m[key]
	if !ok || raw == nil {
		return false, nil
	}
	var text string
	switch x := raw.(type) {
	case json.Number:
		text = x.String()
	case string:
		text = x
	default:
		return false, fmt.Errorf("%w: %s is not an amount", errBadPayload, key)
	}
	var t money.Tiyn
	tiynRaw, hasTiyn := m[key+tiynSuffix]
	if hasTiyn {
		n, ok := tiynRaw.(json.Number)
		if !ok {
			return false, fmt.Errorf("%w: %s%s is not an integer", errBadPayload, key, tiynSuffix)
		}
		i, err := strconv.ParseInt(n.String(), 10, 64)
		if err != nil {
			return false, fmt.Errorf("%w: %s%s is not an integer", errBadPayload, key, tiynSuffix)
		}
		t = money.Tiyn(i)
	} else {
		var err error
		if t, err = money.ParseTenge(text); err != nil {
			return false, fmt.Errorf("%w: %s = %q", errBadPayload, key, text)
		}
	}
	want := t.Tenge()
	_, isString := raw.(string)
	if isString && text == want && hasTiyn {
		return false, nil
	}
	m[key] = want
	if !hasTiyn {
		m[key+tiynSuffix] = json.Number(strconv.FormatInt(int64(t), 10))
	}
	return true, nil
}
