<?php

// Эталон расчётов брони из гостевого приложения старого проекта (Zafiro a680bab), буквальные копии формул:
//   duration() — app/Models/ComputerReservation.php ComputerReservation::durationMinutes()
//   start()    — app/Services/ComputerReservations.php ComputerReservations::startPaidSession()
//   split()    — app/Services/GuestBookings.php GuestBookings::book() (делёж бонусной части по ПК)
//   booked()   — app/Services/AgentTariffPurchaseService.php AgentTariffPurchaseService::bookedMinutes()
// Времена — секунды Unix (getTimestamp()), суммы — тиыны.
//
//   docker run --rm -v "$PWD":/w -w /w php:8.4.27-cli php tools/php-oracle/gen-booking-fixture.php \
//     > server/internal/reservations/testdata/php_booking.json

mt_srand(20261115);

function duration(int $startsAt, int $endsAt): int
{
    return max(1, (int) round(($endsAt - $startsAt) / 60));
}

/** @return array{0: int, 1: int, 2: int, 3: int} duration, minutes, main_cents, bonus_cents */
function start(int $startsAt, int $endsAt, int $now, int $paidMain, int $paidBonus): array
{
    $duration = duration($startsAt, $endsAt);
    $minutes = min($duration, intdiv(max(0, $endsAt - $now) + 59, 60));
    if ($minutes <= 0) {
        return [$duration, $minutes, 0, 0];
    }

    return [$duration, $minutes, intdiv($paidMain * $minutes, $duration), intdiv($paidBonus * $minutes, $duration)];
}

/**
 * @param  list<int>  $prices
 * @return list<array{0: int, 1: int}> на каждый ПК: paid_main_cents, paid_bonus_cents
 */
function split(array $prices, int $bonus): array
{
    $price = array_sum($prices);
    $bonusLeft = $bonus;
    $out = [];
    foreach ($prices as $index => $pcPrice) {
        $pcBonus = $index === count($prices) - 1 ? $bonusLeft : ($price === 0 ? 0 : intdiv($bonus * $pcPrice, $price));
        $bonusLeft -= $pcBonus;
        $out[] = [$pcPrice - $pcBonus, $pcBonus];
    }

    return $out;
}

function booked(int $minutes, int $localStart, int $until): int
{
    return min($minutes, (int) ceil(($until - $localStart) / 60));
}

$base = 1794650400; // 2026-11-15 10:00:00 UTC — кратно 60

$durations = [];
foreach ([-120, -1, 0, 1, 29, 30, 31, 59, 60, 89, 90, 91, 149, 150, 151, 1800, 3599, 3600, 3630, 5400, 43200, 86400, 604800] as $secs) {
    $durations[] = [$base, $base + $secs, duration($base, $base + $secs)];
}
for ($i = 0; $i < 400; $i++) {
    $secs = mt_rand(0, 90000);
    $durations[] = [$base, $base + $secs, duration($base, $base + $secs)];
}

$starts = [];
$addStart = function (int $len, int $late, int $main, int $bonus) use (&$starts, $base): void {
    $starts[] = [$base, $base + $len, $base + $late, $main, $bonus, ...start($base, $base + $len, $base + $late, $main, $bonus)];
};
// GS-544 (за 5 минут до начала), GS-545 (опоздал на 30 минут), GAP-044 (за секунду до конца, после конца).
$addStart(3600, -300, 60000, 0);
$addStart(3600, 1800, 60000, 0);
$addStart(3600, 3599, 60000, 0);
$addStart(3600, 3600, 60000, 0);
$addStart(3600, 3700, 60000, 0);
$addStart(5400, 1801, 45001, 44999);
$addStart(10800, 61, 120000, 1);
for ($i = 0; $i < 600; $i++) {
    $len = 60 * mt_rand(1, 720);
    $addStart($len, mt_rand(-7200, $len + 120), mt_rand(0, 5000000), mt_rand(0, 5000000));
}

$splits = [];
$addSplit = function (array $prices, int $bonus) use (&$splits): void {
    $splits[] = [$prices, $bonus, split($prices, $bonus)];
};
// GAP-041: обычные числа и случай отрицательной основной части последнего ПК.
$addSplit([60000, 30000], 45000);
$addSplit([60000, 60000, 60000], 179999);
$addSplit([60000, 60000, 60000], 180000);
$addSplit([60000, 60000, 60000], 1);
$addSplit([60000], 60000);
$addSplit([0, 0], 0);
$addSplit([120000, 120000], 0);
$addSplit([1, 1, 1, 1, 1, 1, 1, 1, 1, 1], 9);
for ($i = 0; $i < 1500; $i++) {
    $n = mt_rand(1, 10);
    $prices = [];
    for ($j = 0; $j < $n; $j++) {
        $prices[] = mt_rand(0, 3) === 0 ? mt_rand(0, 7) : mt_rand(1, 3000000);
    }
    $total = array_sum($prices);
    $bonus = match (mt_rand(0, 3)) {
        0 => $total,
        1 => max(0, $total - mt_rand(0, 12)),
        default => mt_rand(0, $total),
    };
    $addSplit($prices, $bonus);
}

$bookeds = [];
foreach ([[180, 0, 10800], [600, 3600, 36000], [600, 3600, 36001], [600, 3600, 35999], [60, 0, 1], [60, 0, 59], [60, 0, 60], [60, 0, 61], [60, 0, 0], [60, 100, 40]] as [$m, $from, $to]) {
    $bookeds[] = [$m, $base + $from, $base + $to, booked($m, $base + $from, $base + $to)];
}
for ($i = 0; $i < 300; $i++) {
    $m = mt_rand(1, 1440);
    $to = mt_rand(1, 100000);
    $bookeds[] = [$m, $base, $base + $to, booked($m, $base, $base + $to)];
}

echo json_encode([
    'php' => PHP_VERSION,
    'duration' => $durations,
    'start' => $starts,
    'split' => $splits,
    'booked' => $bookeds,
], JSON_UNESCAPED_SLASHES), "\n";
