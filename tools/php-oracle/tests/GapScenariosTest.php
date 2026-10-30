<?php

/*
 * Pest-оракул: сценарии GAP-001…GAP-036 из docs/golden-scenarios.md zafirogo, прогнанные на старом
 * Laravel (одноразовая копия, см. tools/php-oracle/run.sh). Каждый тест сначала записывает
 * ФАКТИЧЕСКИЙ результат старого кода (gapRecord → JSONL), потом сверяет с числами документа.
 * Имена тестов начинаются с ID сценария. Функции-помощники — с префиксом gap, чтобы не столкнуться
 * с глобальными функциями старых тестов.
 */

use App\Models\ActivityLog;
use App\Models\CashShift;
use App\Models\CashTransaction;
use App\Models\Club;
use App\Models\ClubComputer;
use App\Models\ClubMembership;
use App\Models\ClubZone;
use App\Models\Friendship;
use App\Models\MemberBalance;
use App\Models\PlatformSetting;
use App\Models\Product;
use App\Models\SessionPurchase;
use App\Models\StockMovement;
use App\Models\Tariff;
use App\Models\TimePackage;
use App\Models\User;
use App\Services\AgentTariffPurchaseService;
use App\Services\CashRegisterService;
use App\Services\ClubComputerSessions;
use App\Services\CurrentClub;
use App\Services\Friends;
use App\Services\FriendTransferCodes;
use App\Services\GuestSessions;
use App\Services\LoyaltyProgram;
use App\Services\PaymentRules;
use App\Services\SeatTransfers;
use App\Services\ShiftReport;
use App\Services\TelegramGateway;
use App\Services\TopUpBonus;
use App\Support\PercentLevels;
use App\Support\WeeklyPeriods;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Carbon;
use Illuminate\Support\Facades\Crypt;
use Illuminate\Support\Facades\Http;
use Illuminate\Validation\ValidationException;

uses(RefreshDatabase::class);

// Возвраты считаются до секунды: время стоит, пока тест его не сдвинет.
// Сообщения сервисов вне HTTP-запроса — на русском, как в клубе (APP_LOCALE копии — en).
beforeEach(function () {
    $this->freezeTime();
    app()->setLocale('ru');
});
afterEach(fn () => Carbon::setTestNow());

/** Фактический результат сценария — строка JSONL (ORACLE_GAP_OUT или storage/logs/oracle-gap.jsonl). */
function gapRecord(string $id, array $facts): void
{
    $file = getenv('ORACLE_GAP_OUT') ?: storage_path('logs/oracle-gap.jsonl');
    file_put_contents($file, json_encode(['id' => $id, 'php' => PHP_VERSION, 'facts' => $facts], JSON_UNESCAPED_UNICODE | JSON_PRESERVE_ZERO_FRACTION)."\n", FILE_APPEND | LOCK_EX);
}

/** Выполняет действие; ValidationException → ['ok' => false, 'errors' => …]. */
function gapCatch(callable $action): array
{
    try {
        $value = $action();

        return ['ok' => true, 'value' => $value];
    } catch (ValidationException $e) {
        return ['ok' => false, 'errors' => $e->errors()];
    }
}

/** Сливает настройки финансов в текущий клуб. */
function gapFinances(array $finances): void
{
    $club = Club::query()->findOrFail(app(CurrentClub::class)->id());
    $club->forceFill(['settings' => array_replace_recursive($club->settings ?? [], ['finances' => $finances])])->save();
    app(CurrentClub::class)->set($club);
}

/** Заменяет раздел finances целиком (чтобы проверить отсутствующие ключи). */
function gapSetFinances(array $finances): void
{
    $club = Club::query()->findOrFail(app(CurrentClub::class)->id());
    $club->forceFill(['settings' => array_replace($club->settings ?? [], ['finances' => $finances])])->save();
    app(CurrentClub::class)->set($club);
}

function gapAgentHeaders(User $user, ClubComputer $computer): array
{
    $token = $user->createToken('PC', ['club:'.$computer->club_id])->plainTextToken;

    return ['Authorization' => 'Bearer '.$token, 'Accept' => 'application/json', 'X-Agent-Token' => app(ClubComputerSessions::class)->issueAgentToken($computer)];
}

/** Владелец клуба + клиент с балансами у ПК в зоне с ценой часа; [клиент, ПК, заголовки агента]. */
function gapCustomer(float $main = 1000, float $bonus = 0, float $pricePerHour = 600, string $role = 'client'): array
{
    if (! User::query()->exists()) {
        User::factory()->create(['name' => 'Владелец']); // первый пользователь — владелец клуба
    }
    $customer = User::factory()->create();
    ClubMembership::query()->where('user_id', $customer->id)->update(['role' => $role]);
    MemberBalance::query()->where('user_id', $customer->id)->update(['balance' => $main, 'bonus_balance' => $bonus]);
    $zone = ClubZone::query()->firstOrCreate(['name' => 'Hall'], ['price_per_hour' => $pricePerHour]);
    $computer = ClubComputer::query()->create(['club_zone_id' => $zone->id, 'number' => ClubComputer::query()->count() + 1]);

    return [$customer, $computer, gapAgentHeaders($customer, $computer)];
}

function gapTariff(array $attributes = []): Tariff
{
    return Tariff::query()->create($attributes + [
        'name' => 'Пакет', 'duration_minutes' => 120, 'price' => 1000, 'currency' => 'KZT',
        'payment_method' => 'main_or_bonus', 'discount_enabled' => true, 'is_active' => true,
    ]);
}

function gapBalances(User $user): array
{
    $balance = MemberBalance::query()->where('user_id', $user->id)->sole();

    return [$balance->balance, $balance->bonus_balance];
}

function gapSpent(User $user): ?string
{
    return ClubMembership::query()->where('user_id', $user->id)->sole()->total_spent;
}

function gapEnd(ClubComputer $computer, array $headers)
{
    return test()->postJson(route('api.club-computers.session.end', $computer), ['session_id' => $computer->fresh()->current_session_id], $headers);
}

/** Ошибки валидации из сессии после формы (redirect). */
function gapSessionErrors(): mixed
{
    $errors = session('errors');

    return is_object($errors) ? $errors->getBag('default')->toArray() : $errors;
}

function gapResponse($response): array
{
    return ['status' => $response->status(), 'json' => $response->json()];
}

/* ───────────── SessionSettlement ───────────── */

test('GAP-001 частичная минута: возврат intdiv по каждому балансу', function () {
    [$customer, $computer, $headers] = gapCustomer(100, 50, 500);
    gapFinances(['allow_mixed_payment' => true]);
    $buy = $this->postJson(route('api.guest.minutes.purchase'), ['computer_id' => $computer->id, 'minutes' => 17], $headers);
    $part = SessionPurchase::query()->first();
    $this->travel(320)->seconds();
    $end = gapEnd($computer, $headers);

    gapRecord('GAP-001', ['purchase' => gapResponse($buy), 'part' => $part?->only(['kind', 'minutes', 'main_cents', 'bonus_cents']), 'end' => gapResponse($end), 'balances' => gapBalances($customer)]);
    $end->assertOk()->assertJson(['refunded' => '68.62', 'refunded_bonus' => '28.59', 'kept_minutes' => 0]);
    expect($part->main_cents)->toBe(10000)->and($part->bonus_cents)->toBe(4167)
        ->and(gapBalances($customer))->toBe(['68.62', '36.92']);
});

test('GAP-002 возврат уменьшает накопленные траты', function () {
    [$customer, $computer, $headers] = gapCustomer(1000, 0);
    $this->postJson(route('api.guest.minutes.purchase'), ['computer_id' => $computer->id, 'minutes' => 60, 'balance' => 'main'], $headers)->assertOk();
    $spentAfterPurchase = gapSpent($customer);
    $this->travel(30)->minutes();
    $end = gapEnd($computer, $headers);

    gapRecord('GAP-002', ['spent_after_purchase' => $spentAfterPurchase, 'end' => gapResponse($end), 'balances' => gapBalances($customer), 'total_spent' => gapSpent($customer)]);
    $end->assertOk()->assertJson(['refunded' => '300.00']);
    expect($spentAfterPurchase)->toBe('600.00')
        ->and(gapBalances($customer)[0])->toBe('700.00')
        ->and(gapSpent($customer))->toBe('300.00');
});

test('GAP-003 сохранённые минуты тарифа — intdiv, неполная минута теряется', function () {
    [$customer, $computer, $headers] = gapCustomer(1000, 0);
    $tariff = gapTariff(['usage_limit_enabled' => true, 'usage_limit_minutes' => 43200]);
    $this->postJson(route('api.tariffs.purchase', $tariff), ['computer_id' => $computer->id, 'balance' => 'main'], $headers)->assertOk();
    $this->travel(29 * 60 + 30)->seconds();
    $end = gapEnd($computer, $headers);

    gapRecord('GAP-003', ['end' => gapResponse($end), 'packages' => TimePackage::query()->get(['minutes_left'])->pluck('minutes_left')->all(), 'balances' => gapBalances($customer)]);
    $end->assertOk()->assertJson(['refunded' => '0.00', 'refunded_bonus' => '0.00', 'kept_minutes' => 90]);
    expect(TimePackage::query()->sole()->minutes_left)->toBe(90)->and(gapBalances($customer))->toBe(['0.00', '0.00']);
});

test('GAP-004 поминутка поверх тарифа с лимитом: части с конца', function () {
    [$customer, $computer, $headers] = gapCustomer(2000, 0);
    $tariff = gapTariff(['price' => 500, 'duration_minutes' => 60, 'usage_limit_enabled' => true, 'usage_limit_minutes' => 1440]);
    $boughtAt = now()->copy();
    $this->postJson(route('api.tariffs.purchase', $tariff), ['computer_id' => $computer->id], $headers)->assertOk();
    $this->postJson(route('api.guest.minutes.purchase'), ['computer_id' => $computer->id, 'minutes' => 30], $headers)->assertOk();
    $afterPurchases = gapBalances($customer);
    $this->travel(10)->minutes();
    $end = gapEnd($computer, $headers);
    $package = TimePackage::query()->first();

    gapRecord('GAP-004', ['after_purchases' => $afterPurchases, 'end' => gapResponse($end), 'balances' => gapBalances($customer), 'package' => $package ? ['minutes_left' => $package->minutes_left, 'expires_at' => $package->expires_at->utc()->toIso8601String(), 'expected_expires_at' => $boughtAt->copy()->addMinutes(1440)->utc()->toIso8601String()] : null]);
    $end->assertOk()->assertJson(['refunded' => '300.00', 'refunded_bonus' => '0.00', 'kept_minutes' => 50]);
    expect($afterPurchases[0])->toBe('1200.00')
        ->and(gapBalances($customer)[0])->toBe('1500.00')
        ->and($package->minutes_left)->toBe(50)
        ->and($package->expires_at->getTimestamp())->toBe($boughtAt->copy()->addMinutes(1440)->getTimestamp());
});

test('GAP-005 истечение сессии командой: без расчёта, покупки остаются', function () {
    [$customer, $computer, $headers] = gapCustomer(1000, 0);
    $this->postJson(route('api.guest.minutes.purchase'), ['computer_id' => $computer->id, 'minutes' => 30, 'balance' => 'main'], $headers)->assertOk();
    $this->travel(31)->minutes();
    $this->artisan('club-computers:expire-sessions')->assertSuccessful();

    $facts = ['command' => ['session_id' => $computer->fresh()->current_session_id, 'purchases_left' => SessionPurchase::query()->count(), 'balances' => gapBalances($customer)]];

    // То же через heartbeat (expireIfDue) на втором ПК.
    app('auth')->forgetGuards();
    [$second, $pc2, $headers2] = gapCustomer(1000, 0);
    $this->postJson(route('api.guest.minutes.purchase'), ['computer_id' => $pc2->id, 'minutes' => 30, 'balance' => 'main'], $headers2)->assertOk();
    $this->travel(31)->minutes();
    $beat = app(ClubComputerSessions::class)->heartbeat($pc2->fresh(), ['state' => 'idle'], '127.0.0.1');
    $facts['heartbeat'] = ['session_expired' => $beat['session_expired'], 'session_id' => $pc2->fresh()->current_session_id, 'purchases_left' => SessionPurchase::query()->where('user_id', $second->id)->count(), 'balances' => gapBalances($second)];

    gapRecord('GAP-005', $facts);
    expect($facts['command'])->toBe(['session_id' => null, 'purchases_left' => 1, 'balances' => ['700.00', '0.00']])
        ->and($facts['heartbeat'])->toBe(['session_expired' => true, 'session_id' => null, 'purchases_left' => 1, 'balances' => ['700.00', '0.00']]);
});

/* ───────────── PaymentRules / AgentTariffPurchaseService ───────────── */

test('GAP-006 смешанная без выбора: основного не хватает сверх лимита бонусов → 422', function () {
    [$customer, $computer, $headers] = gapCustomer(500, 1000);
    gapFinances(['allow_mixed_payment' => true, 'bonus_max_percent' => 30]);
    $tariff = gapTariff(['price' => 1000, 'duration_minutes' => 60]);
    $buy = $this->postJson(route('api.tariffs.purchase', $tariff), ['computer_id' => $computer->id], $headers);

    gapRecord('GAP-006', ['purchase' => gapResponse($buy), 'balances' => gapBalances($customer)]);
    $buy->assertUnprocessable()->assertJsonValidationErrors(['balance' => 'Недостаточно средств для покупки тарифа.']);
    expect(gapBalances($customer))->toBe(['500.00', '1000.00']);
});

test('GAP-007 без смешанной и без выбора: бонусы платят всю цену', function () {
    [$customer, $computer, $headers] = gapCustomer(500, 1500);
    gapFinances(['bonus_max_percent' => 100]);
    $tariff = gapTariff(['price' => 1000, 'duration_minutes' => 60]);
    $buy = $this->postJson(route('api.tariffs.purchase', $tariff), ['computer_id' => $computer->id], $headers);

    gapRecord('GAP-007', ['purchase' => gapResponse($buy), 'balances' => gapBalances($customer), 'total_spent' => gapSpent($customer)]);
    $buy->assertOk()->assertJson(['new_balance' => '500.00', 'new_bonus' => '500.00']);
    expect(gapSpent($customer))->toBe('0.00');
});

test('GAP-008 лимит бонусов — intdiv вниз', function () {
    [$customer, $computer, $headers] = gapCustomer(2000, 500);
    gapFinances(['allow_mixed_payment' => true, 'bonus_max_percent' => 30]);
    $tariff = gapTariff(['price' => 999.99, 'duration_minutes' => 60]);
    $buy = $this->postJson(route('api.tariffs.purchase', $tariff), ['computer_id' => $computer->id, 'balance' => 'bonus'], $headers);

    gapRecord('GAP-008', ['bonus_limit_cents' => app(PaymentRules::class)->bonusLimitCents(99999), 'purchase' => gapResponse($buy), 'balances' => gapBalances($customer), 'log' => ActivityLog::query()->where('event', 'tariff.purchased')->first()?->details]);
    expect(app(PaymentRules::class)->bonusLimitCents(99999))->toBe(29999);
    $buy->assertOk()->assertJson(['new_balance' => '1300.00', 'new_bonus' => '200.01']);
});

test('GAP-009 PaymentRules: ограничения значений настроек', function () {
    $rules = fn (): PaymentRules => app(PaymentRules::class);
    $facts = [];
    foreach (['150' => 150, '-5' => -5, 'abc' => 'abc', '30.7' => 30.7] as $label => $value) {
        gapSetFinances(['bonus_max_percent' => $value]);
        $facts['bonus_max_percent'][$label] = $rules()->bonusMaxPercent();
    }
    gapSetFinances([]);
    $facts['bonus_max_percent']['missing'] = $rules()->bonusMaxPercent();
    foreach (['90' => 90, '-1' => -1] as $label => $value) {
        gapSetFinances(['auto_billing_pause_minutes' => $value]);
        $facts['auto_billing_pause_minutes'][$label] = $rules()->autoBillingPauseMinutes();
    }
    gapSetFinances([]);
    $facts['auto_billing_pause_minutes']['missing'] = $rules()->autoBillingPauseMinutes();
    gapSetFinances(['discount_scope' => 'never']);
    $facts['discount_scope']['never'] = $rules()->discountScope();
    gapSetFinances(['bonus_max_percent' => 30]);
    $facts['offered']['30_no_mixed'] = $rules()->bonusPercentOffered();
    gapSetFinances(['bonus_max_percent' => 100]);
    $facts['offered']['100_no_mixed'] = $rules()->bonusPercentOffered();
    $facts['offered']['100_main_only'] = $rules()->bonusPercentOffered(true);

    gapRecord('GAP-009', $facts);
    expect($facts['bonus_max_percent'])->toMatchArray(['150' => 100, '-5' => 0, 'abc' => 100, 'missing' => 100])
        ->and($facts['auto_billing_pause_minutes'])->toBe(['90' => 60, '-1' => 0, 'missing' => 5])
        ->and($facts['discount_scope']['never'])->toBe('tariffs')
        ->and($facts['offered'])->toBe(['30_no_mixed' => 0, '100_no_mixed' => 100, '100_main_only' => 0]);
});

test('GAP-010 скидка на час: двойное округление', function () {
    [$customer, $computer, $headers] = gapCustomer(1000, 0, 555);
    ClubMembership::query()->where('user_id', $customer->id)->update(['discount' => 7.5]);
    gapFinances(['discount_scope' => 'all']);
    $info = $this->getJson(route('api.guest.minutes', ['computer_id' => $computer->id]), $headers);
    $buy = $this->postJson(route('api.guest.minutes.purchase'), ['computer_id' => $computer->id, 'minutes' => 7, 'balance' => 'main'], $headers);

    gapRecord('GAP-010', ['price_per_hour' => $info->json('price_per_hour'), 'purchase' => gapResponse($buy), 'log' => ActivityLog::query()->where('event', 'time.purchased')->first()?->details, 'minutesPriceInCents' => AgentTariffPurchaseService::minutesPriceInCents('513.37', 7)]);
    expect($info->json('price_per_hour'))->toBe('513.37')
        ->and(AgentTariffPurchaseService::minutesPriceInCents('513.37', 7))->toBe(5989);
    $buy->assertOk()->assertJson(['new_balance' => '940.11']);
    expect(ActivityLog::query()->where('event', 'time.purchased')->sole()->details['amount'])->toBe('59.89');
});

test('GAP-011 скидка 100%: тариф бесплатный', function () {
    [$customer, $computer, $headers] = gapCustomer(0, 0);
    ClubMembership::query()->where('user_id', $customer->id)->update(['discount' => 100]);
    $tariff = gapTariff(['price' => 1000, 'duration_minutes' => 60]);
    $buy = $this->postJson(route('api.tariffs.purchase', $tariff), ['computer_id' => $computer->id], $headers);
    $log = ActivityLog::query()->where('event', 'tariff.purchased')->first()?->details;

    gapRecord('GAP-011', ['purchase' => gapResponse($buy), 'log' => $log, 'total_spent' => gapSpent($customer), 'session_minutes' => $computer->fresh()->session_ends_at ? (int) round(now()->diffInMinutes($computer->fresh()->session_ends_at, true)) : null]);
    $buy->assertOk()->assertJson(['new_balance' => '0.00', 'new_bonus' => '0.00', 'minutes_added' => 60]);
    expect($log['amount'])->toBe('0.00')->and(gapSpent($customer))->toBe('0.00');
});

test('GAP-012 накопительная скидка: порог включительно и максимум уровней', function () {
    [$customer, $computer, $headers] = gapCustomer(5000, 0);
    gapFinances(['loyalty_enabled' => true, 'loyalty_levels' => [['spent' => 1500, 'percent' => 10], ['spent' => 3000, 'percent' => 5]]]);
    gapTariff(['price' => 1000, 'duration_minutes' => 60]);
    $facts = [];
    foreach (['1499.99', '1500.00', '3000.00'] as $spent) {
        ClubMembership::query()->where('user_id', $customer->id)->update(['total_spent' => $spent]);
        $membership = ClubMembership::query()->where('user_id', $customer->id)->sole();
        $facts[$spent] = ['percent' => app(LoyaltyProgram::class)->percentFor($membership), 'tariff_price' => $this->getJson(route('api.tariffs.index'), $headers)->json('data.0.price')];
    }

    gapRecord('GAP-012', $facts);
    expect($facts)->toBe([
        '1499.99' => ['percent' => 0.0, 'tariff_price' => '1000.00'],
        '1500.00' => ['percent' => 10.0, 'tariff_price' => '900.00'],
        '3000.00' => ['percent' => 10.0, 'tariff_price' => '900.00'],
    ]);
});

test('GAP-013 уменьшение трат не ниже нуля', function () {
    [$customer] = gapCustomer();
    ClubMembership::query()->where('user_id', $customer->id)->update(['total_spent' => 200]);
    app(LoyaltyProgram::class)->subtractSpending($customer->id, 35000);

    gapRecord('GAP-013', ['total_spent' => gapSpent($customer)]);
    expect(gapSpent($customer))->toBe('0.00');
});

test('GAP-014 запуск пакета ограничен сроком; истёкший пакет', function () {
    [$customer, $computer, $headers] = gapCustomer(0, 0);
    $tariff = gapTariff(['usage_limit_enabled' => true, 'usage_limit_minutes' => 43200]);
    $package = TimePackage::query()->create(['user_id' => $customer->id, 'tariff_id' => $tariff->id, 'name' => 'Пакет', 'minutes_left' => 90, 'expires_at' => now()->addSeconds(40 * 60 + 30)]);
    $expiresAt = $package->fresh()->expires_at->getTimestamp();
    $start = $this->postJson(route('api.guest.packages.start', $package), ['computer_id' => $computer->id], $headers);
    $packageGone = ! TimePackage::query()->whereKey($package->id)->exists();
    $sessionMinutes = intdiv($computer->fresh()->session_ends_at->getTimestamp() - now()->getTimestamp(), 60);
    $this->travel(10)->minutes();
    $end = gapEnd($computer, $headers);
    $kept = TimePackage::query()->first();

    $expired = TimePackage::query()->create(['user_id' => $customer->id, 'tariff_id' => $tariff->id, 'name' => 'Старый', 'minutes_left' => 60, 'expires_at' => now()->subMinute()]);
    $startExpired = $this->postJson(route('api.guest.packages.start', $expired), ['computer_id' => $computer->id], $headers);

    gapRecord('GAP-014', [
        'start' => gapResponse($start), 'package_deleted' => $packageGone, 'session_minutes' => $sessionMinutes,
        'end' => gapResponse($end), 'kept' => $kept ? ['minutes_left' => $kept->minutes_left, 'same_expiry' => $kept->expires_at->getTimestamp() === $expiresAt] : null,
        'start_expired' => gapResponse($startExpired), 'expired_package_still_exists' => TimePackage::query()->whereKey($expired->id)->exists(),
    ]);
    $start->assertOk()->assertJsonPath('minutes_added', 40);
    expect($packageGone)->toBeTrue()->and($sessionMinutes)->toBe(40);
    $end->assertOk()->assertJson(['kept_minutes' => 30]);
    expect($kept->expires_at->getTimestamp())->toBe($expiresAt);
    $startExpired->assertUnprocessable()->assertJsonValidationErrors(['package' => 'Срок абонемента истёк.']);
});

test('GAP-015 тариф только для роли client', function () {
    [$client, $pc1, $clientHeaders] = gapCustomer(2000, 0);
    [$cashier, $pc2, $cashierHeaders] = gapCustomer(2000, 0, role: 'cashier');
    $tariff = gapTariff(['price' => 500, 'duration_minutes' => 60]);
    $tariff->forceFill(['user_roles' => ['client']])->save();

    $clientList = $this->getJson(route('api.tariffs.index'), $clientHeaders);
    $clientBuy = $this->postJson(route('api.tariffs.purchase', $tariff), ['computer_id' => $pc1->id], $clientHeaders);
    app('auth')->forgetGuards();
    app(CurrentClub::class)->clear();
    $cashierList = $this->getJson(route('api.tariffs.index'), $cashierHeaders);
    $cashierBuy = $this->postJson(route('api.tariffs.purchase', $tariff), ['computer_id' => $pc2->id], $cashierHeaders);

    gapRecord('GAP-015', ['client_list_ids' => collect($clientList->json('data'))->pluck('id')->all(), 'client_buy' => gapResponse($clientBuy), 'cashier_list' => gapResponse($cashierList), 'cashier_buy' => gapResponse($cashierBuy), 'balances' => ['client' => gapBalances($client), 'cashier' => gapBalances($cashier)]]);
    expect(collect($clientList->json('data'))->pluck('id')->all())->toBe([(string) $tariff->id]);
    $clientBuy->assertOk();
    expect(collect($cashierList->json('data'))->pluck('id')->all())->toBe([]);
    $cashierBuy->assertUnprocessable()->assertJsonValidationErrors(['tariff' => 'Тариф недоступен.']);
});

test('GAP-016 покупка продлевает активную сессию', function () {
    Carbon::setTestNow(Carbon::parse('2026-10-30 10:00:00', 'UTC'));
    [$customer, $computer, $headers] = gapCustomer(2000, 0);
    $this->postJson(route('api.guest.minutes.purchase'), ['computer_id' => $computer->id, 'minutes' => 60], $headers)->assertOk();
    $first = $computer->fresh();
    $buy = $this->postJson(route('api.guest.minutes.purchase'), ['computer_id' => $computer->id, 'minutes' => 30], $headers);
    $second = $computer->fresh();

    gapRecord('GAP-016', ['first_ends_at' => $first->session_ends_at->utc()->format('H:i'), 'second' => gapResponse($buy), 'ends_at' => $second->session_ends_at->utc()->format('H:i'), 'same_session' => $first->current_session_id === $second->current_session_id, 'parts' => SessionPurchase::query()->where('session_id', $second->current_session_id)->count()]);
    expect($first->session_ends_at->utc()->format('H:i'))->toBe('11:00')
        ->and($second->session_ends_at->utc()->format('H:i'))->toBe('11:30')
        ->and($second->current_session_id)->toBe($first->current_session_id)
        ->and(SessionPurchase::query()->where('session_id', $second->current_session_id)->count())->toBe(2);
});

test('GAP-017 недельные периоды: через конец недели, конец включительно', function () {
    $period = [['start_day' => 'sat', 'start_time' => '22:00', 'end_day' => 'mon', 'end_time' => '06:00']];
    // 2026-10-31 — суббота.
    $at = fn (string $time): bool => WeeklyPeriods::contains($period, Carbon::parse($time, 'UTC'));
    $facts = [
        'sat 21:59' => $at('2026-10-31 21:59:00'),
        'sat 22:00' => $at('2026-10-31 22:00:00'),
        'sun 03:00' => $at('2026-11-01 03:00:00'),
        'mon 06:00' => $at('2026-11-02 06:00:00'),
        'mon 06:00:59' => $at('2026-11-02 06:00:59'),
        'mon 06:01' => $at('2026-11-02 06:01:00'),
        'unknown day' => WeeklyPeriods::contains([['start_day' => 'xyz', 'start_time' => '00:00', 'end_day' => 'sun', 'end_time' => '23:59']], Carbon::parse('2026-10-31 12:00', 'UTC')),
        'empty' => WeeklyPeriods::contains([], Carbon::parse('2026-10-31 12:00', 'UTC')),
        'null' => WeeklyPeriods::contains(null, Carbon::parse('2026-10-31 12:00', 'UTC')),
    ];

    gapRecord('GAP-017', $facts);
    expect($facts)->toBe(['sat 21:59' => false, 'sat 22:00' => true, 'sun 03:00' => true, 'mon 06:00' => true, 'mon 06:00:59' => true, 'mon 06:01' => false, 'unknown day' => false, 'empty' => true, 'null' => true]);
});

/* ───────────── TopUpBonus ───────────── */

test('GAP-018 дробный процент бонуса — половина вверх', function () {
    gapFinances(['topup_bonus_enabled' => true, 'topup_bonus_levels' => [['amount' => 500, 'percent' => 2.5]]]);
    $bonus = app(TopUpBonus::class)->for(999.99);

    gapRecord('GAP-018', ['for(999.99)' => $bonus]);
    expect($bonus)->toBe(['percent' => 2.5, 'cents' => 2500]);
});

test('GAP-019 бонус: порог включительно и максимум уровней', function () {
    gapFinances(['topup_bonus_enabled' => true, 'topup_bonus_levels' => [['amount' => 2000, 'percent' => 5], ['amount' => 5000, 'percent' => 3]]]);
    $facts = [];
    foreach ([1999.99, 2000.00, 6000.00] as $amount) {
        $facts[number_format($amount, 2, '.', '')] = app(TopUpBonus::class)->for($amount);
    }

    gapRecord('GAP-019', $facts);
    expect($facts)->toBe([
        '1999.99' => ['percent' => 0.0, 'cents' => 0],
        '2000.00' => ['percent' => 5.0, 'cents' => 10000],
        '6000.00' => ['percent' => 5.0, 'cents' => 30000],
    ]);
});

test('GAP-020 пополнение владельцем без кассы не даёт бонуса', function () {
    [$customer] = gapCustomer(0, 0);
    gapFinances(['topup_bonus_enabled' => true, 'topup_bonus_levels' => [['amount' => 1000, 'percent' => 10]]]);
    $owner = User::query()->orderBy('id')->first();
    $membership = ClubMembership::query()->where('user_id', $customer->id)->sole();
    $response = $this->actingAs($owner)->patch(route('users.update', $membership), ['name' => $customer->name, 'email' => $customer->email, 'top_up_amount' => '2000', 'top_up_balance' => 'balance']);

    gapRecord('GAP-020', ['session_errors' => gapSessionErrors(), 'balances' => gapBalances($customer), 'cash_transactions' => CashTransaction::query()->count()]);
    $response->assertSessionHasNoErrors();
    expect(gapBalances($customer))->toBe(['2000.00', '0.00'])->and(CashTransaction::query()->count())->toBe(0);
});

test('GAP-021 уровни бонуса: нормализация и не больше 20', function () {
    $normalized = TopUpBonus::normalize([['amount' => '1000.555', 'percent' => '10']]);
    $this->actingAs(User::factory()->create());
    $levels = array_map(fn (int $i): array => ['amount' => (string) ($i * 100), 'percent' => '1'], range(1, 21));
    $save21 = $this->post(route('settings.save'), ['section' => 'finances', 'currency' => 'KZT', 'topup_bonus_enabled' => '1', 'topup_bonus_levels' => $levels]);
    $errors21 = gapSessionErrors();
    $save21->assertSessionHasErrors('topup_bonus_levels');
    $save20 = $this->post(route('settings.save'), ['section' => 'finances', 'currency' => 'KZT', 'topup_bonus_enabled' => '1', 'topup_bonus_levels' => array_slice($levels, 0, 20)]);

    gapRecord('GAP-021', ['normalize' => $normalized, 'round(1000.555, 2)' => round(1000.555, 2), 'errors_21' => $errors21, 'errors_20' => gapSessionErrors()]);
    expect($normalized)->toBe([['amount' => 1000.56, 'percent' => 10]]);
    $save20->assertSessionHasNoErrors();
});

/* ───────────── CashRegisterService ───────────── */

test('GAP-022 возврат: в кассе недостаточно средств', function () {
    [$customer] = gapCustomer(0, 0);
    $owner = User::query()->orderBy('id')->first();
    $cash = app(CashRegisterService::class);
    $shift = $cash->open(0, $owner->id);
    $topUp = $cash->topUpBalance($customer->id, 1000, 'balance', 'cash', $owner->id);
    $cash->transact(['type' => 'expense', 'amount' => 800, 'description' => 'Расход'], $owner->id);
    $refund = gapCatch(fn () => $cash->refund($topUp, $owner->id));

    gapRecord('GAP-022', ['refund' => $refund, 'balances' => gapBalances($customer), 'cash_balance' => $cash->totals($shift)['balance']]);
    expect($refund)->toBe(['ok' => false, 'errors' => ['refund' => ['В кассе недостаточно средств.']]])
        ->and(gapBalances($customer))->toBe(['1000.00', '0.00'])
        ->and($cash->totals($shift)['balance'])->toBe(200.0);
});

test('GAP-023 возврат операции закрытой смены', function () {
    $owner = User::factory()->create();
    $cash = app(CashRegisterService::class);
    $first = $cash->open(0, $owner->id);
    $cola = Product::factory()->create(['name' => 'Cola', 'price' => 700, 'quantity' => 10]);
    $sale = $cash->sale([['product_id' => $cola->id, 'quantity' => 1]], $owner->id);
    $cash->close($first, $owner->id);
    $cash->open(0, $owner->id);
    $refund = gapCatch(fn () => $cash->refund($sale, $owner->id));

    gapRecord('GAP-023', ['refund' => $refund]);
    expect($refund)->toBe(['ok' => false, 'errors' => ['refund' => ['Вернуть можно только операцию открытой смены.']]]);
});

test('GAP-024 возврат пополнения: бонус частично потрачен', function () {
    [$customer] = gapCustomer(0, 0);
    gapFinances(['topup_bonus_enabled' => true, 'topup_bonus_levels' => [['amount' => 1000, 'percent' => 10]]]);
    $owner = User::query()->orderBy('id')->first();
    $cash = app(CashRegisterService::class);
    $cash->open(0, $owner->id);
    $topUp = $cash->topUpBalance($customer->id, 2000, 'balance', 'cash', $owner->id);
    $afterTopUp = gapBalances($customer);
    MemberBalance::query()->where('user_id', $customer->id)->update(['bonus_balance' => 50]);
    $refund = gapCatch(fn () => $cash->refund($topUp, $owner->id));
    $log = ActivityLog::query()->where('event', 'cash.refund')->first()?->details;

    gapRecord('GAP-024', ['after_top_up' => $afterTopUp, 'refund_ok' => $refund['ok'], 'refund_amount' => $refund['ok'] ? $refund['value']->amount : null, 'balances' => gapBalances($customer), 'log' => $log]);
    expect($afterTopUp)->toBe(['2000.00', '200.00'])
        ->and(gapBalances($customer))->toBe(['0.00', '0.00'])
        ->and($log['bonus_taken_back'])->toEqual(50)
        ->and($refund['value']->amount)->toBe('2000.00');
});

test('GAP-025 возврат пополнения бонусного баланса', function () {
    [$customer] = gapCustomer(0, 0);
    gapFinances(['topup_bonus_enabled' => true, 'topup_bonus_levels' => [['amount' => 100, 'percent' => 10]]]);
    $owner = User::query()->orderBy('id')->first();
    $cash = app(CashRegisterService::class);
    $cash->open(0, $owner->id);
    $topUp = $cash->topUpBalance($customer->id, 500, 'bonus_balance', 'cash', $owner->id);
    $afterTopUp = gapBalances($customer);
    $refund = gapCatch(fn () => $cash->refund($topUp, $owner->id));
    $afterRefund = gapBalances($customer);

    $second = $cash->topUpBalance($customer->id, 500, 'bonus_balance', 'cash', $owner->id);
    MemberBalance::query()->where('user_id', $customer->id)->update(['bonus_balance' => 400]);
    $refundShort = gapCatch(fn () => $cash->refund($second, $owner->id));

    gapRecord('GAP-025', ['after_top_up' => $afterTopUp, 'refund_ok' => $refund['ok'], 'after_refund' => $afterRefund, 'refund_short' => $refundShort, 'final' => gapBalances($customer)]);
    expect($afterTopUp)->toBe(['0.00', '500.00'])
        ->and($refund['ok'])->toBeTrue()
        ->and($afterRefund)->toBe(['0.00', '0.00'])
        ->and($refundShort['ok'])->toBeFalse()
        ->and(array_keys($refundShort['errors']))->toBe(['refund']);
});

test('GAP-026 продажа: товара не хватает; смена не открыта', function () {
    $owner = User::factory()->create();
    $cash = app(CashRegisterService::class);
    $cola = Product::factory()->create(['name' => 'Cola', 'price' => 700, 'quantity' => 2]);
    $noShift = gapCatch(fn () => $cash->sale([['product_id' => $cola->id, 'quantity' => 1]], $owner->id));
    $cash->open(0, $owner->id);
    $short = gapCatch(fn () => $cash->sale([['product_id' => $cola->id, 'quantity' => 3]], $owner->id));

    gapRecord('GAP-026', ['no_shift' => $noShift, 'short' => $short, 'stock' => $cola->fresh()->quantity, 'movements' => StockMovement::query()->count(), 'transactions' => CashTransaction::query()->count()]);
    expect($noShift)->toBe(['ok' => false, 'errors' => ['items' => ['Сначала откройте смену.']]])
        ->and($short)->toBe(['ok' => false, 'errors' => ['items' => ['Недостаточно товара: Cola.']]])
        ->and($cola->fresh()->quantity)->toBe(2)
        ->and(StockMovement::query()->count())->toBe(0)
        ->and(CashTransaction::query()->count())->toBe(0);
});

test('GAP-027 закрытие смены с возвратом и расходом', function () {
    [$customer] = gapCustomer(0, 0);
    $owner = User::query()->orderBy('id')->first();
    $cash = app(CashRegisterService::class);
    $shift = $cash->open(1000, $owner->id);
    $cola = Product::factory()->create(['name' => 'Cola', 'price' => 700, 'quantity' => 10]);
    $sale = $cash->sale([['product_id' => $cola->id, 'quantity' => 3]], $owner->id);
    $cash->topUpBalance($customer->id, 5000, 'balance', 'cash', $owner->id);
    $cash->transact(['type' => 'expense', 'amount' => 300, 'description' => 'Вода'], $owner->id);
    $cash->refund($sale, $owner->id);
    $closed = $cash->close($shift, $owner->id);

    gapRecord('GAP-027', ['totals' => $cash->totals($shift), 'closing_balance' => $closed->fresh()->closing_balance]);
    expect($cash->totals($shift))->toBe(['income' => 7100.0, 'expense' => 2400.0, 'balance' => 5700.0])
        ->and($closed->fresh()->closing_balance)->toBe('5700.00');
});

/* ───────────── ShiftReport ───────────── */

test('GAP-028 X/Z-отчёт: прочий приход и возвраты', function () {
    $cashier = User::factory()->create(['name' => 'Айгерим']);
    $cash = app(CashRegisterService::class);
    $shift = $cash->open(0, $cashier->id);
    $cola = Product::factory()->create(['name' => 'Coca Cola', 'price' => 700, 'quantity' => 20]);
    $sale = $cash->sale([['product_id' => $cola->id, 'quantity' => 2]], $cashier->id);
    $cash->transact(['type' => 'income', 'amount' => 300, 'description' => 'Разное'], $cashier->id);
    $pc = ClubComputer::query()->create(['club_zone_id' => ClubZone::query()->create(['name' => 'VIP', 'price_per_hour' => 600])->id, 'number' => 1]);
    app(GuestSessions::class)->sell([$pc->id], null, 60, 'cash', $cashier->id);
    $cash->refund($sale, $cashier->id);
    $report = app(ShiftReport::class)->for($shift);

    gapRecord('GAP-028', $report);
    expect($report['kinds'])->toBe([
        ['label' => 'Продажа товаров', 'count' => 1, 'amount' => 1400.0],
        ['label' => 'Пополнение балансов', 'count' => 0, 'amount' => 0.0],
        ['label' => 'Гостевые сессии', 'count' => 1, 'amount' => 600.0],
        ['label' => 'Прочий приход', 'count' => 1, 'amount' => 300.0],
    ])->and($report['items'])->toBe([])
        ->and($report['refunds'])->toBe(['count' => 1, 'amount' => 1400.0])
        ->and($report['staff'])->toBe([['name' => 'Айгерим', 'count' => 4, 'income' => 2300.0, 'expense' => 1400.0]]);
});

test('GAP-029 X/Z-отчёт: сотрудники по приходу', function () {
    $a = User::factory()->create(['name' => 'А']);
    $b = User::factory()->create(['name' => 'Б']);
    $cash = app(CashRegisterService::class);
    $shift = $cash->open(0, $a->id);
    $cash->transact(['type' => 'income', 'amount' => 500, 'description' => 'А'], $a->id);
    $cash->transact(['type' => 'income', 'amount' => 1500, 'description' => 'Б'], $b->id);
    $cash->transact(['type' => 'expense', 'amount' => 100, 'description' => 'Б расход'], $b->id);
    $report = app(ShiftReport::class)->for($shift);

    gapRecord('GAP-029', ['staff' => $report['staff'], 'discount' => $report['discount']]);
    expect($report['staff'])->toBe([
        ['name' => 'Б', 'count' => 2, 'income' => 1500.0, 'expense' => 100.0],
        ['name' => 'А', 'count' => 1, 'income' => 500.0, 'expense' => 0.0],
    ])->and($report['discount'])->toBe(0.0);
});

/* ───────────── GuestSessions ───────────── */

test('GAP-030 гостевая сессия на два ПК в разных зонах', function () {
    $cashier = User::factory()->create();
    app(CashRegisterService::class)->open(0, $cashier->id);
    $pc1 = ClubComputer::query()->create(['club_zone_id' => ClubZone::query()->create(['name' => 'VIP', 'price_per_hour' => 600])->id, 'number' => 1]);
    $pc2 = ClubComputer::query()->create(['club_zone_id' => ClubZone::query()->create(['name' => 'Hall', 'price_per_hour' => 450])->id, 'number' => 2]);
    $sold = gapCatch(fn () => app(GuestSessions::class)->sell([$pc1->id, $pc2->id], null, 90, 'card', $cashier->id));

    gapRecord('GAP-030', ['amount' => $sold['value']['amount'] ?? $sold, 'transactions' => CashTransaction::query()->pluck('amount')->all()]);
    expect($sold['value']['amount'])->toBe('1575.00')
        ->and(CashTransaction::query()->pluck('amount')->all())->toBe(['1575.00']);
});

test('GAP-031 гостевая сессия на ПК с сессией клиента; завершение гостевой', function () {
    [$customer, $computer, $headers] = gapCustomer(1000, 0);
    $owner = User::query()->orderBy('id')->first();
    $cash = app(CashRegisterService::class);
    $cash->open(0, $owner->id);
    $this->postJson(route('api.guest.minutes.purchase'), ['computer_id' => $computer->id, 'minutes' => 30], $headers)->assertOk();
    $refused = gapCatch(fn () => app(GuestSessions::class)->sell([$computer->id], null, 30, 'cash', $owner->id));
    $transactionsAfterRefusal = CashTransaction::query()->count();

    $guestPc = ClubComputer::query()->create(['club_zone_id' => $computer->club_zone_id, 'number' => 50]);
    app(GuestSessions::class)->sell([$guestPc->id], null, 30, 'cash', $owner->id);
    $this->travel(5)->minutes();
    $ended = app(ClubComputerSessions::class)->end($guestPc->fresh(), (string) $guestPc->fresh()->current_session_id, $owner, true);

    gapRecord('GAP-031', ['refused' => $refused, 'transactions_after_refusal' => $transactionsAfterRefusal, 'guest_end' => $ended, 'guest_pc_session' => $guestPc->fresh()->current_session_id]);
    expect($refused['ok'])->toBeFalse()
        ->and($refused['errors'])->toBe(['computer_ids' => ['ПК '.$computer->displayName().' занят или забронирован.']])
        ->and($transactionsAfterRefusal)->toBe(0)
        ->and($ended)->toBe(['refund_main' => 0, 'refund_bonus' => 0, 'kept_minutes' => 0]);
});

/* ───────────── Friends / FriendTransferCodes ───────────── */

/** Арман и Дана — друзья; [arman, dana, friendship]. */
function gapFriends(float $armanBalance = 100, float $danaBalance = 10): array
{
    $arman = User::factory()->create(['name' => 'Арман', 'phone' => '+77011112233']);
    $dana = User::factory()->create(['name' => 'Дана', 'phone' => '+77029998877']);
    MemberBalance::query()->where('user_id', $arman->id)->update(['balance' => $armanBalance, 'bonus_balance' => 50]);
    MemberBalance::query()->where('user_id', $dana->id)->update(['balance' => $danaBalance, 'bonus_balance' => 0]);
    $friendship = Friendship::query()->create(['requester_id' => $arman->id, 'addressee_id' => $dana->id, 'accepted_at' => now()]);

    return [$arman, $dana, $friendship];
}

test('GAP-032 сумма перевода: формат и максимум', function () {
    $cents = [];
    foreach (['40,50', '10.555', '1000000', '1000000.01', ' 5 ', '0.001', '5.5'] as $input) {
        $cents[$input] = Friends::amountToCents($input);
    }
    [$arman, , $friendship] = gapFriends(1000000, 0);
    $friends = app(Friends::class);
    $tooPrecise = gapCatch(fn () => $friends->transfer($friendship, $arman, '10.555'));
    $tooMuch = gapCatch(fn () => $friends->transfer($friendship, $arman, '1000000.01'));
    $max = gapCatch(fn () => $friends->transfer($friendship, $arman, '1000000'));

    gapRecord('GAP-032', ['cents' => $cents, 'too_precise' => $tooPrecise, 'too_much' => $tooMuch, 'max' => $max]);
    expect($cents)->toMatchArray(['40,50' => 4050, '10.555' => 0, '1000000' => 100000000, '1000000.01' => 100000001, ' 5 ' => 500])
        ->and($tooPrecise)->toBe(['ok' => false, 'errors' => ['amount' => ['Сумма перевода — от 0,01 до 1 000 000.']]])
        ->and($tooMuch)->toBe(['ok' => false, 'errors' => ['amount' => ['Сумма перевода — от 0,01 до 1 000 000.']]])
        ->and($max)->toBe(['ok' => true, 'value' => ['balance' => '0.00', 'amount' => '1000000.00']]);
});

test('GAP-033 перевод другу не влияет на траты и бонусы', function () {
    [$arman, $dana, $friendship] = gapFriends(100, 10);
    $result = app(Friends::class)->transfer($friendship, $arman, '40');

    gapRecord('GAP-033', ['result' => $result, 'arman' => gapBalances($arman), 'dana' => gapBalances($dana), 'spent' => [gapSpent($arman), gapSpent($dana)]]);
    expect(gapBalances($arman))->toBe(['60.00', '50.00'])
        ->and(gapBalances($dana))->toBe(['50.00', '0.00'])
        ->and([gapSpent($arman), gapSpent($dana)])->toBe(['0.00', '0.00']);
});

test('GAP-034 код перевода: срок, лимит в час, только последний код, другая дружба', function () {
    [$arman, $dana, $friendship] = gapFriends(100, 10);
    $bob = User::factory()->create(['name' => 'Боб']);
    $other = Friendship::query()->create(['requester_id' => $arman->id, 'addressee_id' => $bob->id, 'accepted_at' => now()]);
    PlatformSetting::write(TelegramGateway::SETTING, ['token' => Crypt::encryptString('AAGexampleGatewayToken_1234567890')], null);
    $codes = new ArrayObject;
    Http::fake(['gatewayapi.telegram.org/*' => function ($request) use ($codes) {
        $codes[] = $request['code'] ?? null;

        return Http::response(['ok' => true, 'result' => ['request_id' => 'r1']]);
    }]);
    $service = app(FriendTransferCodes::class);
    $facts = [];

    // Срок: код через 5 мин 1 с.
    $service->send($friendship, $arman, '10');
    $this->travel(5 * 60 + 1)->seconds();
    $facts['expired'] = gapCatch(fn () => $service->confirm($friendship, $arman, '10', $codes[0]));
    $facts['balances_after_expired'] = gapBalances($arman);

    // Лимит: через 2 часа (старые коды вне часа) — 5 кодов с интервалом 61 с, 6-й.
    $this->travel(2)->hours();
    $sent = [];
    for ($i = 1; $i <= 5; $i++) {
        $sent[$i] = gapCatch(fn () => $service->send($friendship, $arman, '10'))['ok'];
        $this->travel(61)->seconds();
    }
    $facts['five_sent'] = $sent;
    $facts['sixth'] = gapCatch(fn () => $service->send($friendship, $arman, '10'));

    // Проверяется только последний код; код другой дружбы.
    $last = $codes[count($codes) - 1];
    $previous = $codes[count($codes) - 2];
    $facts['previous_code'] = gapCatch(fn () => $service->confirm($friendship, $arman, '10', $previous));
    $facts['other_friendship'] = gapCatch(fn () => $service->confirm($other, $arman, '10', $last));
    $facts['last_code'] = gapCatch(fn () => $service->confirm($friendship, $arman, '10', $last));
    $facts['balances'] = ['arman' => gapBalances($arman), 'dana' => gapBalances($dana)];

    gapRecord('GAP-034', $facts);
    $wrong = ['ok' => false, 'errors' => ['code' => ['Код неверный или устарел. Запросите новый код.']]];
    expect($facts['expired'])->toBe($wrong)
        ->and($facts['balances_after_expired'])->toBe(['100.00', '50.00'])
        ->and($facts['five_sent'])->toBe([1 => true, 2 => true, 3 => true, 4 => true, 5 => true])
        ->and($facts['sixth'])->toBe(['ok' => false, 'errors' => ['code' => ['Слишком много запросов кода. Попробуйте через час.']]])
        ->and($facts['previous_code'])->toBe($wrong)
        ->and($facts['other_friendship'])->toBe($wrong)
        ->and($facts['last_code']['ok'])->toBeTrue()
        ->and($facts['balances'])->toBe(['arman' => ['90.00', '50.00'], 'dana' => ['20.00', '0.00']]);
});

/* ───────────── SeatTransfers ───────────── */

function gapOnline(ClubComputer $computer): ClubComputer
{
    $computer->forceFill(['last_seen_at' => now()])->save();

    return $computer;
}

test('GAP-035 пересадка: поминутное время остаётся в своей зоне', function () {
    [$customer, $from, $headers] = gapCustomer(1000, 0);
    gapOnline($from);
    $vip = gapOnline(ClubComputer::query()->create(['club_zone_id' => ClubZone::query()->create(['name' => 'VIP', 'price_per_hour' => 900])->id, 'number' => 20]));
    $hall = gapOnline(ClubComputer::query()->create(['club_zone_id' => $from->club_zone_id, 'number' => 21]));
    $this->postJson(route('api.guest.minutes.purchase'), ['computer_id' => $from->id, 'minutes' => 60], $headers)->assertOk();
    $beforeTransfer = gapBalances($customer);
    $seats = app(SeatTransfers::class);
    $toVip = gapCatch(fn () => $seats->transfer($customer, $from->fresh(), $vip->id));
    $toHall = gapCatch(fn () => $seats->transfer($customer, $from->fresh(), $hall->id)->id);

    gapRecord('GAP-035', ['to_vip' => $toVip, 'to_hall' => $toHall, 'before' => $beforeTransfer, 'after' => gapBalances($customer)]);
    expect($toVip['ok'])->toBeFalse()->and(array_keys($toVip['errors']))->toBe(['target_computer_id'])
        ->and($toHall)->toBe(['ok' => true, 'value' => $hall->id])
        ->and(gapBalances($customer))->toBe($beforeTransfer);
});

test('GAP-036 пересадка, затем завершение: возврат по той же сессии', function () {
    [$customer, $from, $headers] = gapCustomer(1000, 0);
    gapOnline($from);
    $target = gapOnline(ClubComputer::query()->create(['club_zone_id' => $from->club_zone_id, 'number' => 30]));
    $this->postJson(route('api.guest.minutes.purchase'), ['computer_id' => $from->id, 'minutes' => 60, 'balance' => 'main'], $headers)->assertOk();
    $sessionId = $from->fresh()->current_session_id;
    $this->travel(10)->minutes();
    gapOnline($target); // ПК шлёт heartbeat: он в сети
    app(SeatTransfers::class)->transfer($customer, $from->fresh(), $target->id);
    $this->travel(20)->minutes();
    app('auth')->forgetGuards();
    $end = gapEnd($target, gapAgentHeaders($customer, $target));

    gapRecord('GAP-036', ['same_session' => $target->fresh()->current_session_id === null ? 'ended' : $target->fresh()->current_session_id, 'session_id_moved' => $sessionId, 'end' => gapResponse($end), 'balances' => gapBalances($customer)]);
    $end->assertOk()->assertJson(['refunded' => '300.00']);
    expect(gapBalances($customer))->toBe(['700.00', '0.00']);
});
