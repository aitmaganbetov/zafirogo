package cash

import (
	"errors"
	"sort"
	"strings"
)

// Тексты ошибок старого кода — ключи перевода (русский текст, как __('…') в Laravel).
// Laravel: app/Services/CashRegisterService.php, app/Http/Controllers/CashRegisterController.php.
const (
	MsgOpenShiftFirst    = "Сначала откройте смену."                                                 // sale(), topUpBalance(), transact()
	MsgShiftAlreadyOpen  = "Смена уже открыта."                                                      // open()
	MsgShiftAlreadyClose = "Смена уже закрыта."                                                      // close()
	MsgNotEnoughStock    = "Недостаточно товара: :product."                                          // sale()
	MsgNotEnoughCash     = "В кассе недостаточно средств."                                           // transact(), refund()
	MsgNotRefundable     = "Эту операцию вернуть нельзя."                                            // refund()
	MsgRefundOpenShift   = "Вернуть можно только операцию открытой смены."                           // refund()
	MsgAlreadyRefunded   = "Эта операция уже возвращена."                                            // refund()
	MsgTopUpSpent        = "Клиент уже потратил часть пополнения: на балансе меньше суммы возврата." // refund()
	MsgPrintZFirst       = "Сначала распечатайте Z-отчёт. Без печати смена не закрывается."          // CashRegisterController::close()
	MsgNoRefundRight     = "Нет права на возвраты. Его даёт владелец клуба в «Настройки → Права»."   // CashRegisterController::refund() (403)
)

// Правила валидации запроса (Laravel FormRequest). Текст сообщения даёт слой валидации (как
// стандартные сообщения Laravel), здесь — только поле и правило.
const (
	RuleRequired = "required"
	RuleNumeric  = "numeric"
	RuleInteger  = "integer"
	RuleMin      = "min"
	RuleMax      = "max"
	RuleGt       = "gt"
	RuleIn       = "in"
	RuleDistinct = "distinct"
	// RuleDecimal — больше двух знаков после точки ('decimal:0,2' Laravel). Решение Q-002 (docs/DECISIONS.md).
	RuleDecimal = "decimal"
)

// Error — ошибка правила кассы с полем формы, как ValidationException::withMessages([field => …]) в Laravel.
// Message — ключ перевода (русский текст) с плейсхолдерами :name, Params — их значения.
// Rule — непустое для ошибок валидации запроса (FormRequest), тогда Message пустое.
type Error struct {
	Field   string
	Message string
	Params  map[string]string
	Rule    string
}

func (e *Error) Error() string {
	if e.Rule != "" {
		return e.Field + ": " + e.Rule
	}
	return e.Field + ": " + e.Text()
}

// Text — сообщение с подставленными параметрами (русский текст; перевод — на границе HTTP).
func (e *Error) Text() string {
	if len(e.Params) == 0 {
		return e.Message
	}
	// Как Laravel makeReplacements(): длинные ключи раньше коротких, чтобы :product не задел :productName.
	keys := make([]string, 0, len(e.Params))
	for k := range e.Params {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	s := e.Message
	for _, k := range keys {
		s = strings.ReplaceAll(s, ":"+k, e.Params[k])
	}
	return s
}

// AsError возвращает *Error из цепочки ошибок.
func AsError(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}

func ruleErr(field, rule string) *Error { return &Error{Field: field, Rule: rule} }

func msgErr(field, msg string) *Error { return &Error{Field: field, Message: msg} }
