// Package money — деньги в целых тиынах и точные эквиваленты PHP-функций,
// через которые старый Laravel-код считал суммы.
//
// Правила (CLAUDE.md, docs/business-rules.md §2, BR-001, BR-002):
//   - сумма — Tiyn (int64), 1 тенге = 100 тиынов; float в API и БД не бывает;
//   - если старая формула считала во float (round($x * 100) и т. п.), воспроизводим её
//     буквально через float64 + PHPRound (семантика PHP 8.4 — версия прода старого Laravel),
//     а не «улучшаем» до целочисленной арифметики;
//   - где старый код целочисленный (intdiv, *_cents), считаем целочисленно (IntDiv).
package money

import "errors"

// Tiyn — сумма в тиынах (1 тенге = 100 тиынов).
type Tiyn int64

// MaxAmount — верхняя граница модуля суммы, с которой работает пакет: 1e15 тиынов
// (10 трлн тенге). Это больше любого decimal(14,2) старой схемы (≈ 1e14 тиынов) и
// заведомо меньше 2^53, поэтому любая допустимая сумма точно представима в float64,
// а произведение суммы на проценты в базисных пунктах (≤ 1e4) не
// переполняет int64 лишь до 9.2e14 (1e15 * 1e4 = 1e19 > MaxInt64 ≈ 9.22e18 — поэтому MulDivRound проверяет переполнение).
const MaxAmount Tiyn = 1_000_000_000_000_000

// Ошибки разбора и проверок.
var (
	// ErrInvalidAmount — строка не является десятичным числом в ожидаемом формате.
	ErrInvalidAmount = errors.New("money: некорректная сумма")
	// ErrTooManyDecimals — больше двух знаков после точки в строгом режиме (ParseTenge).
	ErrTooManyDecimals = errors.New("money: больше двух знаков после точки")
	// ErrOutOfRange — модуль суммы больше MaxAmount или результат не помещается в int64.
	ErrOutOfRange = errors.New("money: сумма вне допустимого диапазона")
)

// Valid сообщает, что |t| ≤ MaxAmount.
func (t Tiyn) Valid() bool { return t >= -MaxAmount && t <= MaxAmount }

// Check возвращает ErrOutOfRange, если |t| > MaxAmount.
func (t Tiyn) Check() error {
	if !t.Valid() {
		return ErrOutOfRange
	}
	return nil
}
