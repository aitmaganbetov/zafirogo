package pricing

import (
	"errors"
	"strings"
)

// Error — ошибка проверки с полем формы/запроса и текстом-ключом перевода, как
// ValidationException::withMessages([field => __(message, params)]) в Laravel.
// Code — машинный код для errors.Is и для API (в старом коде его не было).
type Error struct {
	Code    string
	Field   string
	Message string            // русский текст = ключ t()
	Params  map[string]string // подстановки :name
}

func (e *Error) Error() string {
	msg := e.Message
	for k, v := range e.Params {
		msg = strings.ReplaceAll(msg, ":"+k, v)
	}
	return e.Field + ": " + msg
}

// Is сравнивает по Code: errors.Is(err, pricing.ErrInsufficientFunds) верно и для копии с подстановками.
func (e *Error) Is(target error) bool {
	var t *Error
	return errors.As(target, &t) && t.Code == e.Code
}

func (e *Error) with(params map[string]string) *Error {
	c := *e
	c.Params = params
	return &c
}

// ErrInvalidInput — входные данные вне области, для которой перенесены формулы (отрицательная цена,
// сумма больше money.MaxAmount и т. п.). Это ошибка вызывающего кода, а не ответ клиенту.
var ErrInvalidInput = errors.New("pricing: некорректные входные данные")

// Ошибки старого кода (поле и текст — как в Laravel; ссылки — у мест, где они возникают).
var (
	// AgentTariffPurchaseService::purchase(), GuestSessions::sell().
	ErrTariffUnavailable = &Error{Code: "tariff_unavailable", Field: "tariff", Message: "Тариф недоступен."}
	// AgentTariffPurchaseService::purchase(): зона ПК не из зон тарифа.
	ErrTariffNotForComputer = &Error{Code: "tariff_not_for_computer", Field: "tariff", Message: "Тариф недоступен для этого компьютера."}
	// AgentTariffPurchaseService::ensureComputerFor().
	ErrComputerReserved = &Error{Code: "computer_reserved", Field: "computer_id", Message: "Забронированный компьютер нельзя использовать для сессии."}
	ErrComputerBusy     = &Error{Code: "computer_busy", Field: "computer_id", Message: "На компьютере уже идёт сессия другого пользователя."}
	// AgentTariffPurchaseService::purchaseMinutes(): у зоны нет цены часа.
	ErrNoHourlyPrice = &Error{Code: "no_hourly_price", Field: "minutes", Message: "В этой зоне время поминутно не продаётся."}
	// AgentTariffPurchaseService::sell(): выбор «бонусами».
	ErrMainBalanceOnly   = &Error{Code: "main_balance_only", Field: "balance", Message: "Этот тариф оплачивается только с основного баланса."}
	ErrBonusNotAllowed   = &Error{Code: "bonus_not_allowed", Field: "balance", Message: "В клубе бонусами нельзя оплачивать время."}
	ErrBonusPercentLimit = &Error{Code: "bonus_percent_limit", Field: "balance", Message: "Бонусами можно оплатить только :percent% цены, а смешанная оплата в клубе выключена. Оплатите с основного баланса."}
	ErrBonusNotEnough    = &Error{Code: "bonus_not_enough", Field: "balance", Message: "Бонусов не хватает на всю цену. Оплатите с основного баланса."}
	// AgentTariffPurchaseService::sell(): не хватает основного или бонусного баланса.
	ErrInsufficientFunds = &Error{Code: "insufficient_funds", Field: "balance", Message: "Недостаточно средств для покупки тарифа."}
	// AgentTariffPurchaseService::startPackage().
	ErrPackageExpired     = &Error{Code: "package_expired", Field: "package", Message: "Срок абонемента истёк."}
	ErrPackageNotForPC    = &Error{Code: "package_not_for_computer", Field: "package", Message: "Абонемент не действует на этом компьютере."}
	ErrPackageOutOfPeriod = &Error{Code: "package_out_of_period", Field: "package", Message: "Абонемент сейчас не действует: проверьте дни и время тарифа."}
	// GuestSessions::sell().
	ErrGuestTariffUnavailable = &Error{Code: "guest_tariff_unavailable", Field: "tariff_id", Message: "Тариф недоступен."}
	ErrGuestTariffZone        = &Error{Code: "guest_tariff_zone", Field: "tariff_id", Message: "Тариф не действует в зоне ПК :computer."}
	ErrGuestNoHourlyPrice     = &Error{Code: "guest_no_hourly_price", Field: "minutes", Message: "В зоне ПК :computer время поминутно не продаётся."}
	ErrGuestComputerBusy      = &Error{Code: "guest_computer_busy", Field: "computer_ids", Message: "ПК :computer занят или забронирован."}
	ErrGuestNoComputers       = &Error{Code: "guest_no_computers", Field: "computer_ids", Message: "Выберите ПК на карте клуба."}
)
