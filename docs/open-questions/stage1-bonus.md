# Вопросы Этапа 1: пакет `billing/bonus`

Пакет: `server/internal/billing/bonus` — чистые расчёты без БД: `PercentLevels` (BR-040), `TopUpBonus` (BR-041),
`LoyaltyProgram` (BR-011, BR-042), чтение `finances.*` из `clubs.settings` (BR-003, Q-003), проверка уровней
при сохранении (`SaveSettingsRequest`). Дифференциальный тест — `oracle_test.go` (тег `phporacle`).

## Проверено оракулом

- [x] Режим `direct` (`testdata/oracle_direct.php`, **PHP 8.3.6**, настоящие `app/Support/PercentLevels.php`,
  `app/Services/TopUpBonus.php`, `app/Services/LoyaltyProgram.php` и `illuminate/*` из vendor копии Zafiro, без
  поднятия Laravel): 25 000 входов (seed 20261030) и 3 × 100 000 (seed 1, 2, 3) — **0 расхождений**. Входы: уровни
  с «шумом» (строки, пробелы, экспоненты, `null`, `bool`, вложенные массивы, лишние и повторные ключи, объект
  вместо списка, равные пороги, `1e400`, числа вне int64), суммы на пороге ± 1 тиын, суммы с полутиыном
  (`x.xx5`, `x.xx4999999`), разные записи `*_enabled` (`"0"`, `"false"`, `[]`, `0.1`, `null`), битые `settings`.
  Контроль: подмена `>=` на `>` в `percentAt`, `math.Round` вместо `money.PHPRound` в `normalize` и наивный
  `math.Round($amount*100)` тест ловит (71, 1 117 и 29 расхождений).
- [x] Режим `eval` (`tools/php-oracle/eval.php`, полный Laravel, **PHP 8.4.27** в Docker): 25 000 входов — **0 расхождений**
  (суммы пополнения только с ≤ 2 знаками, см. MQ-1).
- [x] Числа GAP-012, GAP-018, GAP-019, GAP-021 и примеры BR-011/BR-040/BR-041, GS-002 подтверждены прямым вызовом
  старого кода на PHP 8.3.6 — совпадают с `docs/golden-scenarios.md` и `docs/business-rules.md`.
  Пометку «нет в старых тестах — посчитать по коду» у GAP-012/018/019/021 можно заменить на «подтверждено оракулом»
  (владелец документа — агент оракула/правил). GAP-002 и GAP-013 в части `total_spent` — арифметика
  `addSpending/subtractSpending` (без БД), полностью — в интеграционном тесте следующего шага.

## Вопросы

- [x] **BQ-1. Имя метода в документах: `TopUpBonus::forAmount()` не существует, метод называется `for()`.**
  - Решение (оркестратор): имя исправлено на `TopUpBonus::for()` в CLAUDE.md, пакете money и stage1-money.md.
  - Контекст: `app/Services/TopUpBonus.php` (~стр. 29) `public function for(float $amount): array`.
    `forAmount()` упомянут в `CLAUDE.md` (пример комментария), в комментариях `server/internal/money/intmath.go`
    (`MulDivRound`, `BasisPoints`), `server/internal/money/tenge.go` (`FromTengeFloat`) и в `stage1-money.md` MQ-1.
  - Рекомендация: исправить ссылки на `TopUpBonus::for()` (файлы чужие — агенту money/оркестратору).

- [ ] **BQ-2. База бонуса и зачисляемая сумма считаются разными округлениями.**
  - Контекст: `CashRegisterService::topUpBalance()` (~стр. 112–117): основной баланс растёт на `$amount` через
    `increment()` — MySQL округляет строку `(string)$float` до `decimal(12,2)` (money `FromTengeFloatDecimal`);
    бонус считается от `(int) round($amount * 100)` (PHP 8.3, `money.FromTengeFloat`). Ввод `numeric` без ограничения
    знаков (BR-053). На практике оба — «половина от нуля» по десятичной записи, но пути разные (14 значащих цифр у
    `(string)$float` и предокругление до 15 цифр у `round()`).
  - В Go: `TopUpForTengeString(levels, amount)` повторяет PHP буквально; `TopUpForCents(levels, cents)` — если
    касса уже перевела сумму в тиыны.
  - Варианты: (а) касса вызывает `TopUpForTengeString` с той же строкой, что пришла в запросе (буквально, Q-002);
    (б) касса сначала получает тиыны через `ParseTengeRounded` и зовёт `TopUpForCents` — одна база для суммы и бонуса.
  - Рекомендация: (а) до решения по Q-002; если владелец запретит > 2 знаков во вводе (Q-002), пути совпадут.

- [ ] **BQ-3. `clubs.settings` в Postgres как `jsonb` теряет порядок и повторы ключей.**
  - Контекст: `PercentLevels::normalize()` сортирует стабильно; при равных порогах побеждает исходный порядок.
    Если уровни хранятся объектом (`{"a": {...}, "b": {...}}`, а не списком) или с повтором ключей, `jsonb`
    переупорядочит/схлопнет их. Через форму так сохранить нельзя (сохранение пишет список после `normalize()`,
    `distinct` запрещает равные пороги) — только ручная правка БД.
  - Рекомендация: колонку оставить `jsonb`; `tools/migrate-from-mysql` пусть проверяет, что уровни — список без
    равных порогов, и сообщает о прочих (решение — агенту схемы/миграции).

- [ ] **BQ-4. Где живёт проверка уровней при сохранении (`ValidateLevels`)?**
  - Контекст: `app/Http/Requests/SaveSettingsRequest.php` `rules()` (~стр. 32–47), `prepareForValidation()` (~стр. 79–88).
    Сделано здесь, потому что GS-001, GS-028…GS-032, GAP-021 отнесены к `billing`. Повторены: фильтр пустых строк,
    `nullable|array|max:20`, `array:T,percent`, `required|numeric|min:0.01|max:…|distinct` (нестрогое `==` PHP:
    `"1000"` и `"1000.0"` — повтор), middleware `TrimStrings` + `ConvertEmptyStringsToNull`. Не повторены тексты
    сообщений (они в i18n) и экзотика Laravel: `min/max` для нечисловых строк (длина строки), раскрытие `*.spent`
    у элемента-скаляра.
  - Рекомендация: пакет настроек клуба (`club`) вызывает `bonus.ValidateLevels` + `bonus.Normalize`; если владелец
    API настроек решит иначе — перенести функцию туда.

- [ ] **BQ-5. Скидка как float.**
  - Контекст: `LoyaltyProgram::discountFor()` возвращает `float` (max двух процентов), `discounted()` переводит его
    в базисные пункты `(int) round($percent * 100)` (BR-012). BR-011 предлагает хранить скидку в базисных пунктах.
  - В Go: `LoyaltyDiscountFor` возвращает `float64` (буквально), вызывающий переводит `money.BasisPoints(...)`.
    Для процентов с ≤ 2 знаками (`decimal(5,2)` и `normalize()`) перевод точный; хранить `discount` в Postgres в
    базисных пунктах можно без потери — решение агента схемы.

- [ ] **BQ-6. Траты и выключенная программа / бонусный баланс — подтверждение поведения.**
  - Контекст: `addSpending()` копит траты всегда (даже при `loyalty_enabled = false`) и только с основного баланса
    (BR-042, GS-035). `AddSpending/SubtractSpending` здесь — чистая арифметика нового значения `total_spent`;
    в ledger это будут проводки «траты» с ключом идемпотентности (следующий шаг). Вопроса к владельцу нет, пункт —
    напоминание для шага с БД: условие «не ниже 0» в старом коде выполняется двумя UPDATE без блокировки строки
    (`subtractSpending()`, ~стр. 53–63); в Go — одна проводка в транзакции с `FOR UPDATE`/`GREATEST(…, 0)`.
