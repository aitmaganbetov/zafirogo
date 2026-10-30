// Package bonus — чистые расчёты бонусов и лояльности без БД (Этап 1):
//
//   - уровни «порог → процент» (BR-040): Normalize, PercentAt —
//     Laravel: app/Support/PercentLevels.php;
//   - бонус за пополнение основного баланса (BR-041): TopUpFor, TopUpForCents —
//     Laravel: app/Services/TopUpBonus.php;
//   - накопительная скидка и траты (BR-011, BR-042): LoyaltyPercentFor, LoyaltyDiscountFor,
//     AddSpending, SubtractSpending — Laravel: app/Services/LoyaltyProgram.php;
//   - чтение настроек `finances.*` из JSON `clubs.settings` в старом формате (BR-003, допущение Q-003):
//     ParseSettings; проверка уровней при сохранении: ValidateLevels.
//
// Формулы перенесены буквально, включая float-шаги PHP 8.4+ (round() через money.PHPRound,
// (int) через money.PHPIntCast) — допущение Этапа 1: Q-009. Подключение к ledger и БД — следующий шаг.
package bonus
