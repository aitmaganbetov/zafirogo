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
