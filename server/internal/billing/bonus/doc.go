// Package bonus — чистые расчёты бонусов и лояльности без БД (Этап 1):
//
//   - уровни «порог → процент» (BR-040): Normalize, PercentAt —
//     Laravel: app/Support/PercentLevels.php;
//   - бонус за пополнение основного баланса (BR-041): TopUpFor, TopUpForCents —
//     Laravel: app/Services/TopUpBonus.php;
//   - накопительная скидка и траты (BR-011, BR-042): LoyaltyPercentFor, LoyaltyDiscountFor,
//     AddSpending, SubtractSpending — Laravel: app/Services/LoyaltyProgram.php;
//   - чтение настроек `finances.*` из JSON `clubs.settings` (BR-003): ParseSettings — уровни в старом формате
//     и в формате v2 (решение Q-003); проверка уровней при сохранении: ValidateLevels, запись уровней v2: EncodeLevels.
//
// Формулы перенесены буквально, включая float-шаги PHP 8.4+ (round() через money.PHPRound,
// (int) через money.PHPIntCast) — Решение Q-009 (docs/DECISIONS.md). Подключение к ledger и БД — следующий шаг.
package bonus
