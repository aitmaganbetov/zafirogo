<?php

/*
 * Дифференциальный оракул: вызывает чистые функции и сервисы старого Laravel (Zafiro) и отдаёт
 * их результат, чтобы Go-тесты (fuzz) сравнивали свои формулы со старым кодом.
 *
 *   php eval.php --app /путь/к/копии/Zafiro < calls.jsonl > results.jsonl
 *
 * Вход — JSONL: {"id": "любое", "fn": "TopUpBonus.for", "args": {...}} — по строке на вызов.
 * Выход — JSONL в том же порядке: {"id": …, "fn": …, "ok": true, "result": …}
 * или {"id": …, "fn": …, "ok": false, "error": {"type": "validation|exception", "message": …, "errors": {…}}}.
 * Список функций и форматы аргументов — tools/php-oracle/README.md, раздел «Pest-оракул».
 *
 * Приложение поднимается один раз: bootstrap/app.php, консольное ядро, sqlite :memory: + migrate.
 * Настройки клуба (finances) передаются в аргументах и ставятся в CurrentClub на время вызова;
 * сценарии с БД (Purchase.*) выполняются в транзакции, которая откатывается после вызова.
 */

declare(strict_types=1);

$options = getopt('', ['app:']);
$appDir = rtrim((string) ($options['app'] ?? getenv('ZAFIRO_APP') ?: ''), '/');
if ($appDir === '' || ! is_file($appDir.'/bootstrap/app.php') || ! is_file($appDir.'/vendor/autoload.php')) {
    fwrite(STDERR, "eval.php: укажите --app <копия Zafiro с vendor/> (или ZAFIRO_APP)\n");
    exit(2);
}

// Окружение как в phpunit.xml старого проекта: sqlite в памяти, без внешних сервисов.
$env = [
    'APP_ENV' => 'testing', 'APP_DEBUG' => 'false', 'APP_KEY' => 'base64:'.base64_encode(str_repeat('k', 32)),
    'DB_CONNECTION' => 'sqlite', 'DB_DATABASE' => ':memory:', 'DB_URL' => '',
    'CACHE_STORE' => 'array', 'SESSION_DRIVER' => 'array', 'QUEUE_CONNECTION' => 'sync', 'MAIL_MAILER' => 'array',
    'BROADCAST_CONNECTION' => 'null', 'LOG_CHANNEL' => 'null', 'MONITORING_RECORD_REQUESTS' => 'false',
    'PULSE_ENABLED' => 'false', 'TELESCOPE_ENABLED' => 'false', 'NIGHTWATCH_ENABLED' => 'false',
];
foreach ($env as $key => $value) {
    putenv("$key=$value");
    $_ENV[$key] = $_SERVER[$key] = $value;
}

require $appDir.'/vendor/autoload.php';
$app = require $appDir.'/bootstrap/app.php';
$app->make(Illuminate\Contracts\Console\Kernel::class)->bootstrap();

use App\Models\Club;
use App\Models\ClubComputer;
use App\Models\ClubMembership;
use App\Models\ClubZone;
use App\Models\MemberBalance;
use App\Models\SessionPurchase;
use App\Models\Tariff;
use App\Models\User;
use App\Services\AgentTariffPurchaseService;
use App\Services\ClubComputerSessions;
use App\Services\CurrentClub;
use App\Services\Friends;
use App\Services\LoyaltyProgram;
use App\Services\PaymentRules;
use App\Services\SessionSettlement;
use App\Models\TimePackage;
use App\Services\TopUpBonus;
use App\Support\PercentLevels;
use App\Support\WeeklyPeriods;
use Illuminate\Support\Carbon;
use Illuminate\Support\Facades\Artisan;
use Illuminate\Support\Facades\DB;
use Illuminate\Validation\ValidationException;

Artisan::call('migrate', ['--force' => true]);
// Сообщения об ошибках — на русском, как в клубе (APP_LOCALE копии — en).
app()->setLocale('ru');

$club = Club::query()->where('slug', 'legacy')->first() ?? Club::query()->create(['name' => 'Oracle', 'slug' => 'legacy']);

/** Ставит клубу настройки finances (как в Settings → Finances) и делает его текущим. */
$withFinances = function (array $args) use ($club): void {
    $finances = $args['finances'] ?? [];
    $club->forceFill(['settings' => ['finances' => $finances]]);
    if ($club->isDirty()) {
        $club->save();
    }
    app(CurrentClub::class)->set($club);
};

$private = fn (string $class, string $method) => (new ReflectionMethod($class, $method))->getClosure(null);

/** @var array<string, callable(array): mixed> */
$functions = [
    // ── TopUpBonus (app/Services/TopUpBonus.php) ──
    // args: amount (number|string, тенге — как float в PHP), levels [{amount, percent}], enabled (bool, по умолчанию true)
    'TopUpBonus.for' => function (array $a) use ($withFinances): array {
        $withFinances(['finances' => ['topup_bonus_enabled' => $a['enabled'] ?? true, 'topup_bonus_levels' => $a['levels'] ?? []]]);

        return app(TopUpBonus::class)->for((float) $a['amount']);
    },
    'TopUpBonus.normalize' => fn (array $a): array => TopUpBonus::normalize($a['levels'] ?? null),

    // ── PercentLevels (app/Support/PercentLevels.php) ──
    // args: levels, threshold ("amount"|"spent"); percentAt: + cents (int)
    'PercentLevels.normalize' => fn (array $a): array => PercentLevels::normalize($a['levels'] ?? null, $a['threshold'] ?? 'amount'),
    'PercentLevels.percentAt' => fn (array $a): float => PercentLevels::percentAt($a['levels'], $a['threshold'] ?? 'amount', (int) $a['cents']),

    // ── LoyaltyProgram (app/Services/LoyaltyProgram.php) ──
    // args: levels [{spent, percent}], enabled (по умолчанию true), total_spent ("1500.00"), discount (личная, "7.50")
    'LoyaltyProgram.levels' => function (array $a) use ($withFinances): array {
        $withFinances(['finances' => ['loyalty_enabled' => $a['enabled'] ?? true, 'loyalty_levels' => $a['levels'] ?? []]]);

        return app(LoyaltyProgram::class)->levels();
    },
    'LoyaltyProgram.percentFor' => function (array $a) use ($withFinances): float {
        $withFinances(['finances' => ['loyalty_enabled' => $a['enabled'] ?? true, 'loyalty_levels' => $a['levels'] ?? []]]);

        return app(LoyaltyProgram::class)->percentFor((new ClubMembership)->forceFill(['total_spent' => $a['total_spent'] ?? '0', 'discount' => $a['discount'] ?? '0']));
    },
    'LoyaltyProgram.discountFor' => function (array $a) use ($withFinances): float {
        $withFinances(['finances' => ['loyalty_enabled' => $a['enabled'] ?? true, 'loyalty_levels' => $a['levels'] ?? []]]);

        return app(LoyaltyProgram::class)->discountFor((new ClubMembership)->forceFill(['total_spent' => $a['total_spent'] ?? '0', 'discount' => $a['discount'] ?? '0']));
    },

    // ── PaymentRules (app/Services/PaymentRules.php) ──
    // args: finances {discount_scope, bonus_max_percent, allow_mixed_payment, auto_billing, auto_billing_pause_minutes, allow_whole_balance}
    // bonusPercentOffered: + main_balance_only (bool); bonusLimitCents: + price (int, тиыны)
    'PaymentRules.all' => function (array $a) use ($withFinances): array {
        $withFinances($a);
        $rules = app(PaymentRules::class);

        return [
            'discountScope' => $rules->discountScope(),
            'discountOnTariffs' => $rules->discountOnTariffs(),
            'discountOnHourly' => $rules->discountOnHourly(),
            'bonusMaxPercent' => $rules->bonusMaxPercent(),
            'autoBillingEnabled' => $rules->autoBillingEnabled(),
            'autoBillingPauseMinutes' => $rules->autoBillingPauseMinutes(),
            'wholeBalanceAllowed' => $rules->wholeBalanceAllowed(),
            'mixedPaymentAllowed' => $rules->mixedPaymentAllowed(),
            'bonusPercentOffered' => $rules->bonusPercentOffered(false),
            'bonusPercentOfferedMainOnly' => $rules->bonusPercentOffered(true),
        ];
    },
    'PaymentRules.bonusPercentOffered' => function (array $a) use ($withFinances): int {
        $withFinances($a);

        return app(PaymentRules::class)->bonusPercentOffered((bool) ($a['main_balance_only'] ?? false));
    },
    'PaymentRules.bonusLimitCents' => function (array $a) use ($withFinances): int {
        $withFinances($a);

        return app(PaymentRules::class)->bonusLimitCents((int) $a['price']);
    },

    // ── WeeklyPeriods (app/Support/WeeklyPeriods.php) ──
    // args: periods [{start_day, start_time, end_day, end_time}] | null, at ("2026-10-31 22:00:00", местное время клуба)
    'WeeklyPeriods.contains' => fn (array $a): bool => WeeklyPeriods::contains($a['periods'] ?? null, Carbon::parse($a['at'], 'UTC')),
    'WeeklyPeriods.isAlways' => fn (array $a): bool => WeeklyPeriods::isAlways($a['periods'] ?? null),

    // ── AgentTariffPurchaseService (app/Services/AgentTariffPurchaseService.php) ──
    'AgentTariffPurchaseService.amountToCents' => fn (array $a): int => AgentTariffPurchaseService::amountToCents((string) $a['amount']),
    'AgentTariffPurchaseService.formatCents' => fn (array $a): string => AgentTariffPurchaseService::formatCents((int) $a['cents']),
    'AgentTariffPurchaseService.minutesPriceInCents' => fn (array $a): int => AgentTariffPurchaseService::minutesPriceInCents((string) $a['price_per_hour'], (int) $a['minutes']),
    // private static discounted(int $price, float $percent): цена в тиынах минус процент, половина вверх
    'AgentTariffPurchaseService.discounted' => fn (array $a): int => $private(AgentTariffPurchaseService::class, 'discounted')((int) $a['price'], (float) $a['percent']),

    // ── Friends (app/Services/Friends.php) ──
    'Friends.amountToCents' => fn (array $a): int => Friends::amountToCents((string) $a['amount']),

    // ── Сценарий покупки через БД (sell(): выбор балансов, скидка, лимит бонусов) ──
    // args: kind "tariff"|"minutes"; main, bonus (строки тенге, "500.00"); finances {...};
    //   discount (личная, "7.5"), total_spent ("0"), loyalty_levels (вкл., если заданы);
    //   tariff: price ("1000.00"), duration_minutes, payment_method ("main_or_bonus"|"main_balance_only"), discount_enabled (true);
    //   minutes: price_per_hour ("600.00"), minutes; balance: null|"main"|"bonus".
    // Результат: response (как JSON агента) или errors; balances после; part (main_cents, bonus_cents); total_spent.
    'Purchase.run' => function (array $a) use ($club): array {
        $finances = ($a['finances'] ?? []) + (isset($a['loyalty_levels']) ? ['loyalty_enabled' => true, 'loyalty_levels' => $a['loyalty_levels']] : []);
        $club->forceFill(['settings' => ['finances' => $finances]])->save();
        app(CurrentClub::class)->set($club);
        DB::beginTransaction();
        try {
            $user = User::query()->create(['name' => 'Oracle', 'phone' => '+7'.random_int(7000000000, 7999999999), 'email' => uniqid('o', true).'@oracle.test', 'password' => 'x']);
            ClubMembership::query()->create(['user_id' => $user->id, 'role' => $a['role'] ?? 'client', 'discount' => $a['discount'] ?? 0]);
            ClubMembership::query()->where('user_id', $user->id)->update(['total_spent' => $a['total_spent'] ?? 0]);
            MemberBalance::query()->create(['user_id' => $user->id, 'balance' => $a['main'] ?? 0, 'bonus_balance' => $a['bonus'] ?? 0]);
            $zone = ClubZone::query()->create(['name' => 'Oracle', 'price_per_hour' => $a['price_per_hour'] ?? null]);
            $computer = ClubComputer::query()->create(['club_zone_id' => $zone->id, 'number' => 1]);
            $service = app(AgentTariffPurchaseService::class);
            try {
                if (($a['kind'] ?? 'tariff') === 'minutes') {
                    $response = $service->purchaseMinutes($user, $computer->id, (int) $a['minutes'], $a['balance'] ?? null);
                } else {
                    $tariff = Tariff::query()->create([
                        'name' => 'Oracle', 'duration_minutes' => $a['duration_minutes'] ?? 60, 'price' => $a['price'], 'currency' => 'KZT',
                        'payment_method' => $a['payment_method'] ?? 'main_or_bonus', 'discount_enabled' => $a['discount_enabled'] ?? true, 'is_active' => true,
                    ]);
                    $response = $service->purchase($user, $tariff, $computer->id, $a['balance'] ?? null);
                }
                $outcome = ['ok' => true, 'response' => array_intersect_key($response, array_flip(['new_balance', 'new_bonus', 'minutes_added']))];
            } catch (ValidationException $e) {
                $outcome = ['ok' => false, 'errors' => $e->errors()];
            }
            $balance = MemberBalance::query()->where('user_id', $user->id)->sole();
            $part = SessionPurchase::query()->where('user_id', $user->id)->first();

            return $outcome + [
                'balances' => [(string) $balance->balance, (string) $balance->bonus_balance],
                'part' => $part === null ? null : ['main_cents' => $part->main_cents, 'bonus_cents' => $part->bonus_cents, 'minutes' => $part->minutes],
                'total_spent' => (string) ClubMembership::query()->where('user_id', $user->id)->sole()->total_spent,
            ];
        } finally {
            DB::rollBack();
        }
    },

    // ── SessionSettlement::settle() (app/Services/SessionSettlement.php) на БД, транзакция откатывается ──
    // args — сценарий из server/internal/billing/session/testdata/settle_oracle.json:
    //   customer_id, now (UTC, с микросекундами), ends_at|null, parts [{id, user_id, kind, tariff_id, name, minutes,
    //   main_cents, bonus_cents, expires_at}]. Результат: refund_main, refund_bonus, kept_minutes, packages
    //   [{tariff_id, minutes_left, expires_at}] по возрастанию id, total_spent_decrease (тиыны).
    'SessionSettlement.settle' => function (array $a) use ($club): array {
        $club->forceFill(['settings' => []])->save();
        app(CurrentClub::class)->set($club);
        $now = Carbon::parse($a['now'])->utc();
        DB::beginTransaction();
        try {
            Carbon::setTestNow($now);
            $customerId = (int) $a['customer_id'];
            $userIds = array_unique(array_merge([$customerId], array_map(fn (array $p): int => (int) $p['user_id'], $a['parts'] ?? [])));
            foreach ($userIds as $id) {
                User::query()->forceCreate(['id' => $id, 'name' => 'U'.$id, 'phone' => '+7700000'.str_pad((string) $id, 4, '0', STR_PAD_LEFT), 'email' => "u$id@oracle.test", 'password' => 'x']);
                ClubMembership::query()->forceCreate(['user_id' => $id, 'role' => 'client', 'discount' => 0, 'total_spent' => '1000000.00']);
                MemberBalance::query()->forceCreate(['user_id' => $id, 'balance' => '1000000.00', 'bonus_balance' => '1000000.00']);
            }
            foreach (array_unique(array_filter(array_map(fn (array $p) => $p['tariff_id'] ?? null, $a['parts'] ?? []))) as $tariffId) {
                Tariff::query()->forceCreate(['id' => $tariffId, 'name' => 'T'.$tariffId, 'duration_minutes' => 60, 'price' => 100, 'currency' => 'KZT', 'payment_method' => 'main_or_bonus', 'is_active' => true]);
            }
            $zone = ClubZone::query()->create(['name' => 'Oracle']);
            $computer = ClubComputer::query()->create(['club_zone_id' => $zone->id, 'number' => 1]);
            $sessionId = 'oracle-session';
            foreach ($a['parts'] ?? [] as $p) {
                SessionPurchase::query()->forceCreate([
                    'id' => $p['id'], 'session_id' => $sessionId, 'user_id' => $p['user_id'], 'kind' => $p['kind'], 'tariff_id' => $p['tariff_id'] ?? null,
                    'name' => $p['name'], 'minutes' => $p['minutes'], 'main_cents' => $p['main_cents'], 'bonus_cents' => $p['bonus_cents'],
                    'expires_at' => isset($p['expires_at']) ? Carbon::parse($p['expires_at'])->utc() : null,
                ]);
            }
            $spent = fn (): int => AgentTariffPurchaseService::amountToCents((string) ClubMembership::query()->where('user_id', $customerId)->sole()->total_spent);
            $before = $spent();
            $endsAt = isset($a['ends_at']) ? Carbon::parse($a['ends_at'])->utc() : null;
            $result = app(SessionSettlement::class)->settle($computer, $sessionId, $customerId, $endsAt, $customerId);

            return $result + [
                'packages' => TimePackage::query()->orderBy('id')->get()->map(fn (TimePackage $t): array => [
                    'tariff_id' => $t->tariff_id, 'minutes_left' => $t->minutes_left, 'expires_at' => $t->expires_at->utc()->format('Y-m-d\TH:i:s.u\Z'),
                ])->all(),
                'total_spent_decrease' => $before - $spent(),
            ];
        } finally {
            Carbon::setTestNow();
            DB::rollBack();
        }
    },

    // ── Примитивы PHP, на которых стоят формулы (для пакета money) ──
    'PHP.round' => fn (array $a): float => round((float) $a['value'], (int) ($a['precision'] ?? 0)),
    'PHP.roundCents' => fn (array $a): int => (int) round(((float) $a['value']) * 100), // (int) round($x * 100), как TopUpBonus::for()
    'PHP.intdiv' => fn (array $a): int => intdiv((int) $a['a'], (int) $a['b']),
    'PHP.version' => fn (array $a): string => PHP_VERSION,
];

// Библиотечный режим: другой скрипт (fill-settle-fixture.php) берёт таблицу функций.
if (defined('ORACLE_LIBRARY')) {
    return $functions;
}

$flags = JSON_UNESCAPED_UNICODE | JSON_PRESERVE_ZERO_FRACTION | JSON_UNESCAPED_SLASHES;
while (($line = fgets(STDIN)) !== false) {
    $line = trim($line);
    if ($line === '' || $line[0] === '#') {
        continue;
    }
    $call = json_decode($line, true);
    if (! is_array($call)) {
        echo json_encode(['ok' => false, 'error' => ['type' => 'input', 'message' => 'не JSON: '.json_last_error_msg()]], $flags)."\n";

        continue;
    }
    $out = ['id' => $call['id'] ?? null, 'fn' => $call['fn'] ?? null];
    $fn = $functions[$call['fn'] ?? ''] ?? null;
    if ($fn === null) {
        echo json_encode($out + ['ok' => false, 'error' => ['type' => 'input', 'message' => 'неизвестная функция', 'known' => array_keys($functions)]], $flags)."\n";

        continue;
    }
    try {
        $out += ['ok' => true, 'result' => $fn((array) ($call['args'] ?? []))];
    } catch (ValidationException $e) {
        $out += ['ok' => false, 'error' => ['type' => 'validation', 'message' => $e->getMessage(), 'errors' => $e->errors()]];
    } catch (Throwable $e) {
        $out += ['ok' => false, 'error' => ['type' => 'exception', 'class' => $e::class, 'message' => $e->getMessage()]];
    }
    echo json_encode($out, $flags)."\n";
}
