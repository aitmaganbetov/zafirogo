<?php

// Эталон денег подписки платформы старого проекта (Zafiro a680bab): сумма счёта, цена со скидкой, начисление
// реселлеру, срок оплаченного периода. Формулы — буквальные копии:
//   amount()      — app/Services/SubscriptionBilling.php SubscriptionBilling::amount()
//   monthly()     — app/Models/ClubSubscription.php ClubSubscription::monthlyPrice()
//   earning()     — app/Services/SubscriptionBilling.php SubscriptionBilling::markPaid() ($earning)
//   addMonths()   — Carbon addMonthsNoOverflow() (markPaid: $from->copy()->addMonthsNoOverflow($invoice->months)),
//                   здесь — DateTime с той же обрезкой дня (Carbon в образ php-cli не входит).
// Цена и сумма читаются из decimal(12,2) строкой и приводятся к float, как в Eloquent ((float) $plan->price_monthly).
// Числа в фикстуре — тиыны и базисные пункты: [вход…, ответ PHP × 100].
//
//   docker run --rm -v "$PWD":/w -w /w php:8.4.27-cli php tools/php-oracle/gen-subscription-fixture.php \
//     > server/internal/subscription/testdata/php_subscription.json

mt_srand(20261115);

function amount(float $pricePerComputer, int $computers, int $months, int $discountPercent): float
{
    return round($pricePerComputer * $computers * $months * (100 - $discountPercent) / 100);
}

function monthly(string $price, int $discountPercent): float
{
    return round((float) $price * (100 - $discountPercent) / 100, 2);
}

function earning(string $amount, string $resellerPercent): float
{
    return round((float) $amount * (float) $resellerPercent / 100, 2);
}

function addMonths(string $from, int $months): string
{
    $date = new DateTimeImmutable($from, new DateTimeZone('UTC'));
    $day = (int) $date->format('j');
    $first = $date->modify('first day of this month')->modify("+$months months");
    $last = (int) $first->format('t');

    return $first->setDate((int) $first->format('Y'), (int) $first->format('n'), min($day, $last))->format('Y-m-d\TH:i:s\Z');
}

function decimal(int $hundredths): string
{
    return sprintf('%d.%02d', intdiv($hundredths, 100), $hundredths % 100);
}

function tiyn(float $tenge): int
{
    return (int) round($tenge * 100);
}

$months = [1, 3, 6, 12];
$amounts = [];
$add = function (int $price, int $computers, int $m, int $discount) use (&$amounts): void {
    $amounts["$price|$computers|$m|$discount"] = [$price, $computers, $m, $discount, tiyn(amount((float) decimal($price), $computers, $m, $discount))];
};
// Известные примеры: GS-552, GS-556, GS-559, GAP-037.
foreach ([[100000, 3, 3, 10], [100000, 2, 6, 0], [100000, 2, 1, 0], [125000, 3, 1, 15], [99999, 3, 1, 0], [99999, 1, 1, 0],
    [100000, 5, 12, 100], [100000, 1, 1, 0], [5000000, 1, 1, 20], [1500000, 40, 12, 7]] as $case) {
    $add(...$case);
}
// Цены в целых тенге: все скидки и сроки, разное число ПК.
foreach ([100000, 125000, 1500000, 777700] as $price) {
    foreach ($months as $m) {
        for ($discount = 0; $discount <= 100; $discount++) {
            foreach ([1, 3, 7, 50, 250] as $computers) {
                $add($price, $computers, $m, $discount);
            }
        }
    }
}
// Цены с тиынами: случайные и ровно на половине тенге (числитель % 10000 = 5000).
for ($i = 0; $i < 1500; $i++) {
    $add(mt_rand(1, 99999999), mt_rand(1, 2000), $months[mt_rand(0, 3)], mt_rand(0, 100));
}
for ($found = 0; $found < 1500;) {
    [$price, $computers, $m, $discount] = [mt_rand(1, 9999999), mt_rand(1, 500), $months[mt_rand(0, 3)], mt_rand(0, 99)];
    if ($price * $computers * $m * (100 - $discount) % 10000 === 5000) {
        $add($price, $computers, $m, $discount);
        $found++;
    }
}

$monthly = [];
foreach ([[5000000, 20], [5000000, 0], [100000, 10], [100500, 50], [35, 10], [205, 10], [99999, 15], [100000, 100]] as [$price, $discount]) {
    $monthly[] = [$price, $discount, tiyn(monthly(decimal($price), $discount))];
}
foreach ([100000, 150000, 1234500, 5000000, 99900] as $price) {
    for ($discount = 0; $discount <= 100; $discount++) {
        $monthly[] = [$price, $discount, tiyn(monthly(decimal($price), $discount))];
    }
}
for ($i = 0; $i < 1000; $i++) {
    [$price, $discount] = [mt_rand(1, 999999999), mt_rand(0, 100)];
    $monthly[] = [$price, $discount, tiyn(monthly(decimal($price), $discount))];
}
for ($found = 0; $found < 1000;) {
    [$price, $discount] = [mt_rand(1, 99999999), mt_rand(0, 99)];
    if ($price * (100 - $discount) % 100 === 50) {
        $monthly[] = [$price, $discount, tiyn(monthly(decimal($price), $discount))];
        $found++;
    }
}

$earnings = [];
// GAP-040: 4810 ₸ × 12,35 %; процент 0 и 100.
foreach ([[481000, 1235], [481000, 0], [481000, 10000], [810000, 1000], [1200000, 1550], [100, 1], [100, 50], [99999999900, 9999]] as [$a, $bp]) {
    $earnings[] = [$a, $bp, tiyn(earning(decimal($a), decimal($bp)))];
}
for ($i = 0; $i < 1000; $i++) {
    [$a, $bp] = [mt_rand(1, 99999999) * 100, mt_rand(0, 10000)];
    $earnings[] = [$a, $bp, tiyn(earning(decimal($a), decimal($bp)))];
}
for ($found = 0; $found < 1000;) {
    [$a, $bp] = [mt_rand(1, 9999999) * 100, mt_rand(0, 10000)];
    if ($a * $bp % 10000 === 5000) {
        $earnings[] = [$a, $bp, tiyn(earning(decimal($a), decimal($bp)))];
        $found++;
    }
}

$periods = [];
foreach (['2026-01-31T10:00:00Z', '2028-01-31T23:59:59Z', '2026-11-15T07:00:00Z', '2026-08-31T00:00:00Z', '2026-12-31T12:30:00Z',
    '2027-02-28T00:00:00Z', '2028-02-29T08:00:00Z', '2026-03-30T00:00:00Z', '2026-05-31T18:45:10Z', '2026-10-29T03:00:00Z'] as $from) {
    foreach ($months as $m) {
        $periods[] = [$from, $m, addMonths($from, $m)];
    }
}

echo json_encode(['php' => PHP_VERSION, 'amount' => array_values($amounts), 'monthly' => $monthly, 'earning' => $earnings, 'add_months' => $periods]), "\n";
