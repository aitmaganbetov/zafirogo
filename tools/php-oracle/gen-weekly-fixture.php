<?php

// Эталон WeeklyPeriods::endOf() старого проекта (коммит b4f08f6 «Tariffs: time ends with the validity period»).
// contains(), isAlways(), minutes() и endOf() — буквальные копии из ../Zafiro/app/Support/WeeklyPeriods.php;
// Carbon заменён тройкой (dayOfWeekIso, hour, minute), а результат — минуты от пн 00:00 этой недели
// (в PHP: $localNow->copy()->subDays(dayOfWeekIso - 1)->startOfDay()->addMinutes($end)); null — конца нет.
//
//   php tools/php-oracle/gen-weekly-fixture.php > server/internal/billing/pricing/testdata/php_weekly_endof.json

const DAYS = ['mon' => 0, 'tue' => 1, 'wed' => 2, 'thu' => 3, 'fri' => 4, 'sat' => 5, 'sun' => 6];
const WEEK = 7 * 1440;

function minutes(string $time): int
{
    return preg_match('/^(\d{2}):(\d{2})$/', $time, $matches) ? (int) $matches[1] * 60 + (int) $matches[2] : 0;
}

function contains(?array $periods, int $dayOfWeekIso, int $hour, int $minute): bool
{
    if (! is_array($periods) || $periods === []) {
        return true;
    }

    $minuteOfWeek = ($dayOfWeekIso - 1) * 1440 + $hour * 60 + $minute;
    foreach ($periods as $period) {
        $startDay = DAYS[$period['start_day'] ?? ''] ?? null;
        $endDay = DAYS[$period['end_day'] ?? ''] ?? null;
        if ($startDay === null || $endDay === null) {
            continue;
        }

        $start = $startDay * 1440 + minutes($period['start_time'] ?? '');
        $end = $endDay * 1440 + minutes($period['end_time'] ?? '');
        if ($start <= $end ? $minuteOfWeek >= $start && $minuteOfWeek <= $end : $minuteOfWeek >= $start || $minuteOfWeek <= $end) {
            return true;
        }
    }

    return false;
}

function isAlways(?array $periods): bool
{
    if (! is_array($periods) || $periods === []) {
        return true;
    }
    foreach ($periods as $period) {
        if (($period['start_day'] ?? '') === 'mon' && ($period['start_time'] ?? '') === '00:00'
            && ($period['end_day'] ?? '') === 'sun' && ($period['end_time'] ?? '') === '23:59') {
            return true;
        }
    }

    return false;
}

function endOf(?array $periods, int $dayOfWeekIso, int $hour, int $minute): ?int
{
    if (isAlways($periods) || ! contains($periods, $dayOfWeekIso, $hour, $minute)) {
        return null;
    }

    $ranges = [];
    foreach ($periods as $period) {
        $startDay = DAYS[$period['start_day'] ?? ''] ?? null;
        $endDay = DAYS[$period['end_day'] ?? ''] ?? null;
        if ($startDay === null || $endDay === null) {
            continue;
        }

        $start = $startDay * 1440 + minutes($period['start_time'] ?? '');
        $end = $endDay * 1440 + minutes($period['end_time'] ?? '') + (($period['end_time'] ?? '') === '23:59' ? 1 : 0);
        if ($end < $start) {
            $end += WEEK;
        }
        foreach ([-WEEK, 0, WEEK] as $shift) {
            $ranges[] = [$start + $shift, $end + $shift];
        }
    }

    $end = ($dayOfWeekIso - 1) * 1440 + $hour * 60 + $minute;
    do {
        $grown = false;
        foreach ($ranges as [$rangeStart, $rangeEnd]) {
            if ($rangeStart <= $end && $rangeEnd > $end) {
                [$end, $grown] = [$rangeEnd, true];
            }
        }
    } while ($grown);

    return $end;
}

mt_srand(20261115);
$days = array_keys(DAYS);
$times = ['00:00', '00:01', '06:00', '08:00', '12:00', '18:00', '22:00', '23:58', '23:59', '9:00', '24:00', '99:99', ''];
$randomTime = fn (): string => mt_rand(0, 3) === 0 ? $times[mt_rand(0, count($times) - 1)] : sprintf('%02d:%02d', mt_rand(0, 23), mt_rand(0, 59));
$randomDay = fn (): string => mt_rand(0, 40) === 0 ? 'xyz' : $days[mt_rand(0, 6)];

$cases = [];
for ($i = 0; $i < 2000; $i++) {
    $periods = [];
    for ($n = mt_rand(1, 4); $n > 0; $n--) {
        $day = $randomDay();
        $periods[] = [
            'start_day' => $day,
            'start_time' => $randomTime(),
            // Чаще тот же день или следующий: так периоды соседствуют и пересекаются.
            'end_day' => mt_rand(0, 2) === 0 ? $randomDay() : (mt_rand(0, 1) === 0 ? $day : $days[((DAYS[$day] ?? 0) + 1) % 7]),
            'end_time' => $randomTime(),
        ];
    }
    if (mt_rand(0, 49) === 0) {
        $periods[] = ['start_day' => 'mon', 'start_time' => '00:00', 'end_day' => 'sun', 'end_time' => '23:59'];
    }
    // Момент: случайный или граница одного из периодов (± минута).
    if (mt_rand(0, 1) === 0) {
        [$d, $h, $m] = [mt_rand(1, 7), mt_rand(0, 23), mt_rand(0, 59)];
    } else {
        $p = $periods[mt_rand(0, count($periods) - 1)];
        $edge = mt_rand(0, 1) === 0 ? ['start_day', 'start_time'] : ['end_day', 'end_time'];
        $at = ((DAYS[$p[$edge[0]]] ?? 0) * 1440 + minutes($p[$edge[1]]) + mt_rand(-1, 1) + WEEK) % WEEK;
        [$d, $h, $m] = [intdiv($at, 1440) + 1, intdiv($at % 1440, 60), $at % 60];
    }
    // Строка: [[start_day, start_time, end_day, end_time], …], dayOfWeekIso, hour, minute, contains, endOf.
    $cases[] = [array_map('array_values', $periods), $d, $h, $m, contains($periods, $d, $h, $m), endOf($periods, $d, $h, $m)];
}

echo json_encode(['php' => PHP_VERSION, 'source' => 'Zafiro b4f08f6 app/Support/WeeklyPeriods.php', 'cases' => $cases], JSON_UNESCAPED_UNICODE), "\n";
