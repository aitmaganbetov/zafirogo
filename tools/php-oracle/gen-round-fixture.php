<?php

/**
 * Оракул PHP для пакета server/internal/money (round(), (int), number_format(), хелперы центов).
 *
 * Запуск (нужен PHP той же ветки, что в проде старого Laravel — 8.4; фикстура снята на 8.4.27):
 *   php tools/php-oracle/gen-round-fixture.php                   # пишет server/internal/money/testdata/php_round.json
 *   php tools/php-oracle/gen-round-fixture.php --full FILE.json  # все ≥ 200 000 кейсов round() для дифференциального теста
 *   php -- --full - < tools/php-oracle/gen-round-fixture.php     # то же в stdout (так его запускает Go-тест, в т.ч. через Docker)
 *
 * Детерминирован: mt_srand(фиксированное зерно), MT19937 одинаков во всех PHP ≥ 7.1.
 * float передаются как 16 hex-символов IEEE-754 (big-endian), чтобы сравнивать побитно.
 * Описание и порядок работы — tools/php-oracle/README.md, раздел «round()».
 */

declare(strict_types=1);

const SEED = 20261030;

if (PHP_INT_SIZE !== 8) {
    fwrite(STDERR, "нужен 64-битный PHP\n");
    exit(1);
}

function hexOf(float $x): string
{
    return bin2hex(pack('E', $x));
}

function fromBits(int $bits): float
{
    return unpack('E', pack('J', $bits))[1];
}

function bitsOf(float $x): int
{
    return unpack('J', pack('E', $x))[1];
}

/** Соседний double вверх (+1) или вниз (-1) по битам, только для конечных положительных. */
function nextUp(float $x, int $dir): float
{
    return fromBits(bitsOf($x) + $dir);
}

/**
 * «Наивное» округление, как в Go math.Round(x*10^p)/10^p (половина от нуля, без предокругления).
 * Нужно только чтобы отметить кейсы, где наивный перенос даёт другой результат.
 */
function naiveRound(float $x, int $places): float
{
    if (! is_finite($x) || $x == 0.0) {
        return $x;
    }
    $f = 10.0 ** abs($places);
    $t = $places >= 0 ? $x * $f : $x / $f;
    if (! is_finite($t)) {
        return $x;
    }
    $a = abs($t);
    $fl = floor($a);
    $r = ($a - $fl >= 0.5) ? $fl + 1.0 : $fl;
    $r = $t < 0 ? -$r : $r;

    return $places >= 0 ? $r / $f : $r * $f;
}

/** Генератор кандидатов round(): [float, places, категория]. */
function roundCandidates(): Generator
{
    mt_srand(SEED);
    // 1. x.xx5 (суммы с полутиыном) — places 2, ± (50 000 × 2).
    for ($i = 0; $i < 50000; $i++) {
        $x = ($i * 10 + 5) / 1000;
        yield [$x, 2, 'x.xx5'];
        yield [-$x, 2, 'x.xx5'];
    }
    // 2. x.xx5 * 100 → round(…, 0): так считали центы (int) round($amount * 100) (25 000).
    for ($i = 0; $i < 25000; $i++) {
        $x = ($i * 10 + 5) / 1000;
        yield [$x * 100, 0, 'x.xx5*100'];
    }
    // 3. x.xxx5 — places 3 (20 000).
    for ($i = 0; $i < 20000; $i++) {
        yield [($i * 10 + 5) / 10000, 3, 'x.xxx5'];
    }
    // 4. Крупные суммы x.xx5 вблизи пределов decimal(12,2)/(14,2) (20 000).
    for ($i = 0; $i < 20000; $i++) {
        $whole = mt_rand(0, 999999999999);
        $x = (float) ($whole.'.'.mt_rand(0, 99).'5');
        yield [$x, 2, 'big x.xx5'];
        if ($i % 2 === 0) {
            yield [$x * 100, 0, 'big x.xx5*100'];
        }
    }
    // 5. Произведения a*b/100 (цена × процент) и a*100 — places 2 и 0 (40 000).
    for ($i = 0; $i < 20000; $i++) {
        $a = mt_rand(1, 100000000) / 100;   // сумма с копейками
        $b = mt_rand(1, 10000) / 100;       // процент 0.01…100
        yield [$a * $b / 100, 2, 'a*b/100'];
        yield [$a * 100, 0, 'a*100'];
    }
    // 6. Соседи половин: k + 0.5 ± 1 ulp, k/100 + 0.005 ± 1 ulp (24 000).
    for ($i = 0; $i < 4000; $i++) {
        $k = mt_rand(0, 10 ** mt_rand(1, 15));
        $h = $k + 0.5;
        yield [nextUp($h, -1), 0, 'half±ulp'];
        yield [nextUp($h, 1), 0, 'half±ulp'];
        $c = ($k % 1000000000) / 100 + 0.005;
        yield [nextUp($c, -1), 2, 'half±ulp'];
        yield [$c, 2, 'half±ulp'];
        yield [nextUp($c, 1), 2, 'half±ulp'];
        yield [-nextUp($c, 1), 2, 'half±ulp'];
    }
    // 7. Очень большие и очень малые, любые places, включая |places| ≥ 23 (ветка strtod) (30 000).
    for ($i = 0; $i < 30000; $i++) {
        $x = (mt_rand() / mt_getrandmax() + mt_rand(1, 9)) * (10 ** mt_rand(-30, 30));
        if (mt_rand(0, 1) === 1) {
            $x = -$x;
        }
        $p = match ($i % 4) {
            0 => mt_rand(-5, 10),
            1 => mt_rand(-30, 30),
            2 => mt_rand(-400, 400),
            default => mt_rand(0, 3),
        };
        yield [$x, $p, 'scale'];
    }
    // 8. Случайные битовые шаблоны конечных double (15 000).
    for ($i = 0; $i < 15000; $i++) {
        $bits = (mt_rand(0, 0x7FFFFFFF) << 32) | mt_rand(0, 0xFFFFFFFF);
        $x = fromBits($bits);
        if (! is_finite($x)) {
            continue;
        }
        yield [$x, mt_rand(-20, 20), 'bits'];
    }
    // 9. Степени десяти и соседи — границы php_intlog10abs (2 000+).
    for ($e = -320; $e <= 308; $e++) {
        $x = (float) ('1e'.$e);
        if ($x == 0.0 || ! is_finite($x)) {
            continue;
        }
        foreach ([-1, 0, 1] as $d) {
            $y = $d === 0 ? $x : nextUp($x, $d);
            yield [$y, mt_rand(-25, 25), 'pow10'];
            yield [$y * 5, 2, 'pow10'];
            // places у границ условия предокругления (precision_places = 14 - floor(log10|x|)):
            // ошибка floor(log10) на единицу здесь меняет результат.
            foreach ([14 - $e, -1 - $e] as $base) {
                foreach ([-1, 0, 1] as $dp) {
                    yield [$y * 1.5, $base + $dp, 'pow10'];
                    yield [$y * 9.5, $base + $dp, 'pow10'];
                }
            }
        }
    }
    // 10. Известные примеры из docs/business-rules.md §2 BR-001, баг GH-12143 (исправлен в 8.4)
    //     и ветки 8.4: places == 0 у целого, |x·10^p| в [1e15, 1e16) (8.3 возвращал x как есть), 0.145 * 100.
    foreach ([[12.345, 2], [100.005 * 100, 0], [1.005 * 100, 0], [2.675, 2], [1.005, 2], [1.955, 2], [5.055, 2],
        [0.285, 2], [1.4999999999999999, 0], [0.49999999999999994, 0], [-0.49999999999999994, 0], [-0.5, 0], [0.5, 0],
        [1.45, 1], [-1.45, 1], [5.0e15 + 0.5, 0], [4503599627370495.5, 0], [1e15, 2], [999999999999.995, 2],
        [PHP_FLOAT_MIN, 2], [PHP_FLOAT_MAX, 2], [PHP_FLOAT_MAX, -308], [PHP_FLOAT_EPSILON, 16], [5e-324, 400],
        [123456.789, -2], [1234.5678, PHP_INT_MAX], [1234.5678, PHP_INT_MIN], [1234.5678, -2147483648], [0.1 + 0.2, 1],
        [0.145 * 100, 0], [0.285 * 100, 0], [3.0, 0], [-1e20, 0], [123456789012345.67, 1], [-1234567890123.4565, 3],
        [0.285, 10], [1.5, 0], [-2.5, 0], [12345678.9, -23], [0.5e-20, 23],
    ] as [$x, $p]) {
        yield [$x, $p, 'known'];
    }
}

/** round() как вызывает его PHP-код; places > C int насыщается так же, как в PHP_FUNCTION(round). */
function caseRow(float $x, int $p, string $cat): array
{
    $r = round($x, $p);
    $n = naiveRound($x, max(-400, min(400, $p)));

    return [hexOf($x), $p, hexOf($r), $cat, bitsOf($r) !== bitsOf($n)];
}

// ---- Буквальные копии старых функций (источник: ../Zafiro, только чтение) ----

// Laravel: app/Services/AgentTariffPurchaseService.php amountToCents()
function legacyAmountToCents(string $amount): int
{
    [$whole, $fraction] = array_pad(explode('.', $amount, 2), 2, '00');

    return ((int) $whole * 100) + (int) str_pad(substr($fraction, 0, 2), 2, '0');
}

// Laravel: app/Services/AgentTariffPurchaseService.php formatCents()
function legacyFormatCents(int $cents): string
{
    return sprintf('%d.%02d', intdiv($cents, 100), $cents % 100);
}

// Laravel: app/Services/Friends.php amountToCents()
function friendsAmountToCents(string $amount): int
{
    $amount = str_replace(',', '.', trim($amount));
    if (! preg_match('/^\d{1,10}(\.\d{1,2})?$/', $amount)) {
        return 0;
    }

    [$whole, $fraction] = array_pad(explode('.', $amount, 2), 2, '00');

    return ((int) $whole * 100) + (int) str_pad($fraction, 2, '0');
}

// Laravel: app/Services/Friends.php storedCents()
function friendsStoredCents(string $amount): int
{
    return (int) round((float) $amount * 100);
}

// Laravel: app/Services/Friends.php formatCents()
function friendsFormatCents(int $cents): string
{
    return ($cents < 0 ? '-' : '').sprintf('%d.%02d', intdiv(abs($cents), 100), abs($cents) % 100);
}

// ---- Строковые входы для (int)/(float)/хелперов ----

function stringCases(): array
{
    mt_srand(SEED + 1);
    $s = ['', '0', '00', '1', '-1', '+1', '1.5', '1.50', '1.505', '12.999', '-1.50', '-0.50', '0.07', '1234.5', '1234.50',
        ' 12', "\t12", "\n12", "\v12", "\f12", "\r12", '12 ', '12abc', 'abc', '.5', '-.5', '5.', '.', '-', '+', 'e5', '1e3',
        '1e', '1e+', '1E-2', '1.5e3x', '0x1A', '9223372036854775807', '9223372036854775808', '-9223372036854775808',
        '-9223372036854775809', '99999999999999999999', '1.9e30', '-1.9e30', '1e400', '-1e400', '1e-400', 'inf', 'nan',
        '1,5', '1 000', '1000,5', ' 1000.5 ', "1000.5\n", '00012.30', '0000000000000000000001', '123456789012.34',
        '9999999999.99', '10000000000', '1000000', '1000000.01', '0.001', '0.00', '0,07', '7,', '7.', '٣', '１'];
    for ($i = 0; $i < 300; $i++) {
        $s[] = mt_rand(0, 99999999).'.'.str_pad((string) mt_rand(0, 99), 2, '0', STR_PAD_LEFT);
        $s[] = '-'.mt_rand(0, 9999).'.'.mt_rand(0, 999);
        $s[] = mt_rand(0, 9999).','.mt_rand(0, 99);
    }

    return $s;
}

function intCases(): array
{
    mt_srand(SEED + 2);
    $c = [0, 1, -1, 5, -5, 50, -50, 99, -99, 100, -100, 101, -101, 150, -150, 123450, -123450, PHP_INT_MAX, PHP_INT_MIN + 1];
    for ($i = 0; $i < 300; $i++) {
        $c[] = mt_rand(-10 ** mt_rand(1, 15), 10 ** mt_rand(1, 15));
    }

    return $c;
}

function floatCasesForCast(): array
{
    mt_srand(SEED + 3);
    $c = [0.0, -0.0, 0.5, -0.5, 1.9999999999999998, -1.9999999999999998, 9.2233720368547758e18, -9.2233720368547758e18,
        9.3e18, -9.3e18, 1e19, -1e19, 1.8446744073709552e19, 2e19, -2e19, 3.6893488147419103e19, 1e30, -1e30, 1e300, -1e300,
        123.456, 4503599627370496.5, 9007199254740993.0, 1e14, 1e15, 99999999999999.9, 999999999999999.0, 0.0001,
        0.00001, 0.000123456789012345, -1.5e-7, 1e25, 1.5e100, -2.5e-300, 5e-324, PHP_FLOAT_MAX, 0.1 + 0.2, 1 / 3, NAN, INF, -INF];
    for ($i = 0; $i < 300; $i++) {
        $c[] = (mt_rand() / mt_getrandmax() - 0.5) * (10 ** mt_rand(0, 25));
    }

    return $c;
}

/** TypeError (переполнение int → float в функции `: int`) → null. */
function orNull(callable $f): ?int
{
    try {
        return $f();
    } catch (TypeError) {
        return null;
    }
}

// ---- main ----

$full = null;
if (($argv[1] ?? '') === '--full') {
    $full = $argv[2] ?? null;
    if ($full === null) {
        fwrite(STDERR, "usage: --full FILE.json | --full -\n");
        exit(2);
    }
}

$all = [];
$byCat = [];
foreach (roundCandidates() as [$x, $p, $cat]) {
    $row = caseRow($x, $p, $cat);
    $all[] = $row;
    $byCat[$cat][] = count($all) - 1;
}

if ($full !== null) {
    file_put_contents($full === '-' ? 'php://stdout' : $full, json_encode(['php' => PHP_VERSION, 'round' => $all], JSON_THROW_ON_ERROR));
    fwrite(STDERR, sprintf("full: %d кейсов round() → %s\n", count($all), $full));
    exit(0);
}

// Отбор показательных кейсов: все «известные»; ВСЕ расхождения наивного math.Round в «денежных»
// категориях (суммы с полутиыном, ×100, произведения — places 0…3); по 60 кейсов из каждой категории;
// выборка ~1500 расхождений из прочих категорий (масштабы, битовые шаблоны, соседи половин).
const MONEY_CATS = ['x.xx5', 'x.xx5*100', 'x.xxx5', 'big x.xx5', 'big x.xx5*100', 'a*b/100'];
$pick = [];
foreach ($byCat as $cat => $idx) {
    if ($cat === 'known') {
        foreach ($idx as $i) {
            $pick[$i] = true;
        }
        continue;
    }
    $step = max(1, intdiv(count($idx), 60));
    for ($k = 0; $k < count($idx); $k += $step) {
        $pick[$idx[$k]] = true;
    }
}
$naive = array_keys(array_filter($all, fn (array $r): bool => $r[4]));
$otherNaive = [];
foreach ($naive as $i) {
    if (in_array($all[$i][3], MONEY_CATS, true)) {
        $pick[$i] = true;
    } else {
        $otherNaive[] = $i;
    }
}
$naiveStep = max(1, intdiv(count($otherNaive), 1500));
for ($k = 0; $k < count($otherNaive); $k += $naiveStep) {
    $pick[$otherNaive[$k]] = true;
}
ksort($pick);
$round = [];
foreach (array_keys($pick) as $i) {
    $round[] = $all[$i];
}

$strings = stringCases();
$ints = intCases();
$floats = floatCasesForCast();
// Суммы для number_format/FromTengeFloat: x.xx5, суммы в тиынах/100, отрицательные.
$amounts = [];
foreach ($round as $r) {
    if (in_array($r[3], ['x.xx5', 'big x.xx5', 'a*b/100', 'half±ulp', 'known'], true)) {
        $amounts[] = unpack('E', hex2bin($r[0]))[1];
    }
}
$amountStep = max(1, intdiv(count($amounts), 1500));
$amounts = array_values(array_filter($amounts, fn (int $k): bool => $k % $amountStep === 0, ARRAY_FILTER_USE_KEY));
foreach ($ints as $i) {
    if (abs($i) <= 10 ** 15) {
        $amounts[] = $i / 100;
    }
}

$out = [
    'php' => PHP_VERSION,
    'generator' => 'tools/php-oracle/gen-round-fixture.php',
    'total_round_cases' => count($all),
    'total_naive_mismatches' => count($naive),
    // [x, places, round(x, places), категория, наивный math.Round даёт другое]
    'round' => $round,
    // [x, (int) x, (string) x]
    'int_cast' => array_map(fn (float $f): array => [hexOf($f), (int) $f, (string) $f], $floats),
    // [x, (int) round(x * 100), number_format(x, 2, '.', ''), (string) x]
    'tenge_float' => array_map(fn (float $f): array => [hexOf($f), (int) round($f * 100), number_format($f, 2, '.', ''), (string) $f], $amounts),
    // [s, (int) s, (float) s, LegacyAmountToCents(s), Friends::amountToCents(s), Friends::storedCents(s), is_numeric(s)]
    'strings' => array_map(fn (string $s): array => [$s, (int) $s, hexOf((float) $s), orNull(fn (): int => legacyAmountToCents($s)), friendsAmountToCents($s), friendsStoredCents($s), is_numeric($s)], $strings),
    // [c, legacy formatCents(c), Friends::formatCents(c), number_format(c / 100, 2, '.', '')]
    'cents' => array_map(fn (int $c): array => [$c, legacyFormatCents($c), $c === PHP_INT_MIN ? '' : friendsFormatCents($c), number_format($c / 100, 2, '.', '')], $ints),
];

$path = __DIR__.'/../../server/internal/money/testdata/php_round.json';
// Одна строка массива — одна строка файла: дифф в git читаем.
$json = "{\n";
$first = true;
foreach ($out as $key => $value) {
    $json .= ($first ? '' : ",\n").json_encode($key).': ';
    $first = false;
    if (! is_array($value)) {
        $json .= json_encode($value, JSON_UNESCAPED_SLASHES | JSON_THROW_ON_ERROR);
        continue;
    }
    $json .= "[\n".implode(",\n", array_map(fn (array $row): string => json_encode($row, JSON_UNESCAPED_UNICODE | JSON_THROW_ON_ERROR), $value))."\n]";
}
$json .= "\n}\n";
file_put_contents($path, $json);
fwrite(STDERR, sprintf("round: %d из %d (наивных расхождений всего %d); → %s\n", count($round), count($all), count($naive), realpath($path)));
