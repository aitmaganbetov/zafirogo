# tools/php-oracle — эталонные ответы старого PHP-кода

## round()

Скрипт `gen-round-fixture.php` снимает с живого PHP ответы функций, через которые старый Laravel-код
считал деньги, и пишет фикстуру `server/internal/money/testdata/php_round.json`. Go-тесты пакета
`server/internal/money` читают её без PHP.

Что в фикстуре (снята на **PHP 8.3.6**, поле `php`):

| Ключ | Строка | Что проверяет Go |
|---|---|---|
| `round` | `[x, places, round(x, places), категория, наивный_math.Round_ошибается]` | `money.PHPRound` побитно |
| `int_cast` | `[x, (int) x, (string) x]` | `PHPIntCast`, `PHPFloatString` |
| `tenge_float` | `[x, (int) round(x*100), number_format(x, 2, '.', ''), (string) x]` | `FromTengeFloat`, `FormatFloat2`, `PHPFloatString` |
| `strings` | `[s, (int) s, (float) s, amountToCents(s), Friends::amountToCents(s), Friends::storedCents(s), is_numeric(s)]` | `PHPStringToInt`, `PHPStringToFloat`, `LegacyAmountToCents`, `ParseTransferAmount`, `StoredCents`, `PHPIsNumeric` |
| `cents` | `[c, formatCents(c), Friends::formatCents(c), number_format(c/100, 2, '.', '')]` | `LegacyFormatCents`, `Tiyn.Tenge` |

`float` хранятся как 16 hex-символов IEEE-754 (big-endian, `bin2hex(pack('E', $x))`), чтобы сравнение было побитным.
`null` в `amountToCents` — PHP бросил `TypeError` (переполнение `int`), в Go это `ErrOutOfRange`.
Функции старого кода в скрипте — буквальные копии из `../Zafiro/app/Services/AgentTariffPurchaseService.php`
и `app/Services/Friends.php`.

Кандидаты (≈ 310 000, детерминированно, `mt_srand(20261030)`): суммы `x.xx5` и `x.xxx5`, `x.xx5 * 100`, крупные суммы
до `decimal(12,2)`, произведения `a*b/100` и `a*100`, соседи половин `k + 0.5 ± 1 ulp`, очень большие/малые значения с
`places` от −400 до 400 (ветка `strtod` в `_php_math_round`), случайные битовые шаблоны, степени десяти и `places` на
границах условия предокругления. В фикстуру попадают: известные примеры, **все** расхождения наивного `math.Round`
в «денежных» категориях, по 60 кейсов каждой категории и ~1500 расхождений из прочих.

### Регенерация

```sh
php tools/php-oracle/gen-round-fixture.php            # перезаписывает testdata/php_round.json
cd server && go test ./internal/money/                # фикстура без PHP
```

Регенерировать только тем же минорным PHP, что в проде старого Laravel (8.3): в PHP 8.4 `round()` переписан,
и часть ответов другая (см. `docs/open-questions/stage1-money.md`).

### Полный дифференциальный тест

```sh
cd server && ZAFIRO_PHP_ORACLE=1 go test -run TestPHPRoundOracleFull -v ./internal/money/
```

Тест запускает `gen-round-fixture.php --full <tmp>` и сравнивает все ≈ 310 000 кейсов `round()` с `money.PHPRound` побитно.
Последний прогон (PHP 8.3.6, 2026-10-30): 310 438 кейсов, расхождений 0; наивный `math.Round(x*10^p)/10^p` ошибся бы в 15 358 из них (только `places` 0…3).

## Pest-оракул

Прогон эталонных сценариев на **старом Laravel** (одноразовая копия `../Zafiro`, сам проект не меняется) и
дифференциальный оракул для Go. Эталон для Go-тестов — фактический результат старого кода (Q-006).

| Файл | Что делает |
|---|---|
| `run.sh` | копирует `../Zafiro` (без `.git`, `mobileclient`, `mobileemployee`, `WindowsAgent`) во временную папку, ставит `vendor/`, `.env` + `key:generate`, подкладывает `tests/*.php` в `tests/Feature/Oracle/`, гоняет Pest и печатает итог |
| `tests/GapScenariosTest.php` | GAP-001…GAP-036 из `docs/golden-scenarios.md`; каждый тест пишет фактический результат (JSONL) и сверяет с документом |
| `eval.php` | дифференциальный оракул: JSONL-вызовы функций старого кода → JSONL-ответы |
| `fill-settle-fixture.php` | заполняет поле `php` в `server/internal/billing/session/testdata/settle_oracle.json` (44 сценария `SessionSettlement::settle()`) |

### PHP 8.4, а не 8.3

`composer.lock` старого проекта требует **PHP ≥ 8.4.1** (`symfony/*` 8.x, PHPUnit 12): на PHP 8.3 зависимости не
ставятся, а `vendor/` не парсится. Поэтому оракул исполняется на PHP 8.4 (`mirror.gcr.io/library/php:8.4.27-cli`,
если локальный `php` < 8.4). Composer нужен только для скачивания (`--ignore-platform-req=php`). В PHP 8.4 `round()`
переписан: на корпусе `gen-round-fixture.php --full` (310 438 кейсов) 8.3 и 8.4 расходятся в 13 471, например
`round(14.499999999999998)` = 15 в 8.3 и 14 в 8.4, то есть `TopUpBonus::for(0.145)` даёт 15 и 14 тиынов соответственно.
Вопрос — в `docs/open-questions/stage1-oracle.md` (S1-ORC-01).

### Запуск

```sh
tools/php-oracle/run.sh                     # GAP-тесты; в конце — факты GAP в JSONL
ORACLE_GAP_RESULT=/tmp/gap.jsonl tools/php-oracle/run.sh   # факты в файл
tools/php-oracle/run.sh all                 # базовая линия: весь Pest старого проекта + GAP (собирает фронт через npm, если есть)
tools/php-oracle/run.sh eval < calls.jsonl > results.jsonl
tools/php-oracle/run.sh settle              # перезаполнить "php" в settle_oracle.json, затем:
cd server && go test ./internal/billing/session/ -run Oracle
```

Переменные: `ZAFIRO_SRC` (по умолчанию `../Zafiro`), `ORACLE_WORK` + `ORACLE_KEEP=1` (оставить копию),
`ORACLE_VENDOR_CACHE` (кэш `vendor/` по хешу `composer.lock`, по умолчанию `~/.cache/zafirogo-php-oracle`),
`PHP_IMAGE`. Если GitHub-архивы пакетов недоступны (прокси отвечает 403), `run.sh` ставит пакеты из исходников
без кэша composer и удаляет `.git` (иначе `vendor/` занимает ~7 ГБ; после очистки ~1.1 ГБ).

### Базовая линия (2026-10-30, PHP 8.4.27, sqlite `:memory:`)

- Полный набор Pest старого проекта: **522 прошло, 6 упало** (3875 проверок) — с собранным фронтом (`npm run build`).
  Без сборки фронта: 374 прошло, 154 упало, из них 142 — `Vite manifest not found` (окружение, не код).
- 6 падений — не денежные и не от копии кода:
  - 3 — нет папки `WindowsAgent` (копия её не включает): `Unit/AgentWindowsAccountsTest`, два теста `AgentLocalizationTest`;
  - 3 — `ServerMonitoringTest` («an alert is sent once…», «a high share of 5xx errors…», «requests notice a silent scheduler…»):
    оповещений отправлено больше, чем ждёт тест (2 вместо 1, 3 вместо 2). Не чинились.
- GAP-тесты: **36 из 36 прошли** (158 проверок).

### Итог GAP (подробно — строка «Оракул» под каждым GAP в `docs/golden-scenarios.md`)

Подтверждены числа всех 36 сценариев. Исправлены побочные утверждения документа:
GAP-001 (остатки деления 0.745 и 0.705 тиына, а не 0.7 и 0.7), GAP-010 (без промежуточного округления — 5989.375, тоже 5989),
GAP-014 (истёкший пакет при 422 **не удаляется**: удаление откатывается вместе с транзакцией). Невыполнимых нет.

### eval.php — формат

Вход — по строке JSON на вызов: `{"id": <любое>, "fn": "<функция>", "args": {...}}`; пустые строки и строки с `#` пропускаются.
Выход — строка на вызов в том же порядке: `{"id", "fn", "ok": true, "result": …}` или
`{"id", "fn", "ok": false, "error": {"type": "validation"|"exception"|"input", "message", "errors"?}}`.
Числа PHP `float` выводятся с `.0` (`JSON_PRESERVE_ZERO_FRACTION`), кратчайшим представлением (`serialize_precision = -1`).
Приложение поднимается один раз (`bootstrap/app.php`, консольное ядро, sqlite `:memory:` + `migrate`, локаль `ru`);
настройки клуба (`finances`) передаются в аргументах; сценарии с БД выполняются в транзакции, которая откатывается.

| `fn` | `args` | `result` | Старый код |
|---|---|---|---|
| `TopUpBonus.for` | `amount` (число/строка тенге → `float`), `levels` `[{amount, percent}]`, `enabled` (по умолч. `true`) | `{percent: float, cents: int}` | `TopUpBonus::for()` |
| `TopUpBonus.normalize` | `levels` | уровни `[{amount, percent}]` | `TopUpBonus::normalize()` |
| `PercentLevels.normalize` | `levels`, `threshold` (`amount`/`spent`) | уровни | `PercentLevels::normalize()` |
| `PercentLevels.percentAt` | `levels`, `threshold`, `cents` | `float` | `PercentLevels::percentAt()` |
| `LoyaltyProgram.levels` | `levels`, `enabled` | уровни в силе | `LoyaltyProgram::levels()` |
| `LoyaltyProgram.percentFor` | `levels`, `enabled`, `total_spent` (`"1500.00"`) | `float` | `LoyaltyProgram::percentFor()` |
| `LoyaltyProgram.discountFor` | + `discount` (личная, `"7.50"`) | `float` | `LoyaltyProgram::discountFor()` |
| `PaymentRules.all` | `finances` `{discount_scope, bonus_max_percent, allow_mixed_payment, auto_billing, auto_billing_pause_minutes, allow_whole_balance}` | все публичные значения `PaymentRules` | `PaymentRules` |
| `PaymentRules.bonusPercentOffered` | `finances`, `main_balance_only` | `int` | `bonusPercentOffered()` |
| `PaymentRules.bonusLimitCents` | `finances`, `price` (тиыны) | `int` | `bonusLimitCents()` |
| `WeeklyPeriods.contains` | `periods` `[{start_day, start_time, end_day, end_time}]`/`null`, `at` (`"2026-10-31 22:00:00"`, местное) | `bool` | `WeeklyPeriods::contains()` |
| `WeeklyPeriods.isAlways` | `periods` | `bool` | `WeeklyPeriods::isAlways()` |
| `AgentTariffPurchaseService.amountToCents` | `amount` (строка) | `int` | `amountToCents()` |
| `AgentTariffPurchaseService.formatCents` | `cents` | строка (`-50` → `"0.-50"`, Q-004) | `formatCents()` |
| `AgentTariffPurchaseService.minutesPriceInCents` | `price_per_hour` (строка), `minutes` | `int` | `minutesPriceInCents()` |
| `AgentTariffPurchaseService.discounted` | `price` (тиыны), `percent` (`float`) | `int` | `discounted()` (private, через Reflection) |
| `Friends.amountToCents` | `amount` | `int` (0 — неверный формат) | `Friends::amountToCents()` |
| `Purchase.run` | `kind` (`tariff`/`minutes`), `main`, `bonus`, `finances`, `discount`, `total_spent`, `loyalty_levels`, `role`; тариф: `price`, `duration_minutes`, `payment_method`, `discount_enabled`; минуты: `price_per_hour`, `minutes`; `balance` (`null`/`main`/`bonus`) | `{ok, response{new_balance,new_bonus,minutes_added} \| errors, balances, part{main_cents,bonus_cents,minutes}, total_spent}` | `AgentTariffPurchaseService::purchase()/purchaseMinutes()` → `sell()` |
| `SessionSettlement.settle` | сценарий из `settle_oracle.json`: `customer_id`, `now`, `ends_at`, `parts[]` | `{refund_main, refund_bonus, kept_minutes, packages[{tariff_id, minutes_left, expires_at}], total_spent_decrease}` | `SessionSettlement::settle()` |
| `PHP.round` / `PHP.roundCents` / `PHP.intdiv` / `PHP.version` | `value`, `precision` / `value` / `a`, `b` / — | как PHP | `round()`, `(int) round($x*100)`, `intdiv()` |

Пример (проверено 2026-10-30 на 23 вызовах, включая GAP-008/010 через `Purchase.run` и GS-046 через `SessionSettlement.settle`):

```jsonl
{"id":1,"fn":"TopUpBonus.for","args":{"amount":999.99,"levels":[{"amount":500,"percent":2.5}]}}
→ {"id":1,"fn":"TopUpBonus.for","ok":true,"result":{"percent":2.5,"cents":2500}}
{"id":16,"fn":"AgentTariffPurchaseService.discounted","args":{"price":55500,"percent":7.5}}
→ {"id":16,"fn":"AgentTariffPurchaseService.discounted","ok":true,"result":51337}
```

`settle_oracle.json` (2026-10-30): 44 из 44 сценариев — ответ PHP совпал с Go (`go test ./internal/billing/session/ -run Oracle`).
