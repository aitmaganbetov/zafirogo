<?php

/**
 * Загрузчик демо-данных (tools/visual/fixtures/demo.json) в КОПИЮ старого Laravel-проекта Zafiro.
 *
 *   php seed.php --app /app --fixture /visual/fixtures/demo.json
 *
 * Запускается после `php artisan migrate:fresh` в копии (tools/visual/old.sh). Справочники пишутся
 * DB::table() с явными id (те же id в новом сервере: одинаковые URL /club-computers/5 и т. п.).
 * Кассовые операции выполняются сервисом старого кода App\Services\CashRegisterService с
 * Carbon::setTestNow(at) — суммы, чеки, остатки товаров и журнал получаются логикой старого кода.
 * Даты в БД — UTC (config app.timezone = UTC), как пишет старый код.
 */

declare(strict_types=1);

use App\Models\CashShift;
use App\Models\Club;
use App\Services\CashRegisterService;
use App\Services\CurrentClub;
use Carbon\Carbon;
use Carbon\CarbonImmutable;
use Illuminate\Support\Facades\DB;

$opts = getopt('', ['app:', 'fixture:']);
$appDir = rtrim((string) ($opts['app'] ?? ''), '/');
$fixture = (string) ($opts['fixture'] ?? '');
if ($appDir === '' || ! is_file($appDir.'/bootstrap/app.php') || ! is_file($fixture)) {
    fwrite(STDERR, "usage: php seed.php --app <копия Zafiro> --fixture <demo.json>\n");
    exit(2);
}

require $appDir.'/vendor/autoload.php';
$app = require $appDir.'/bootstrap/app.php';
$app->make(Illuminate\Contracts\Console\Kernel::class)->bootstrap();
app()->setLocale('ru');

$data = json_decode((string) file_get_contents($fixture), true, 512, JSON_THROW_ON_ERROR);

/** Местное время ISO 8601 → строка UTC, как хранит Laravel. */
function utc(?string $at): ?string
{
    return $at === null ? null : CarbonImmutable::parse($at)->utc()->format('Y-m-d H:i:s');
}

/** Сумма-строка тенге ("1500.00") → decimal-строка, как в MySQL decimal. */
function tenge(?string $amount, string $default = '0.00'): string
{
    return $amount ?? $default;
}

function jsonOrNull(mixed $v): ?string
{
    return $v === null ? null : json_encode($v, JSON_UNESCAPED_UNICODE | JSON_THROW_ON_ERROR);
}

$now = CarbonImmutable::parse($data['now']);
$created = fn (array $row, string $key = 'created_at'): string => utc($row[$key] ?? null) ?? $now->utc()->format('Y-m-d H:i:s');
$hash = $data['login']['password_bcrypt'];

DB::transaction(function () use ($data, $created, $hash): void {
    // Миграции старого кода создают клуб «legacy» и тариф trial (backfill_*): демо-данные начинаются с чистой базы.
    DB::table('club_subscriptions')->delete();
    DB::table('clubs')->delete();
    DB::table('saas_plans')->delete();
    foreach ($data['saas_plans'] as $p) {
        DB::table('saas_plans')->insert([
            'id' => $p['id'], 'slug' => $p['slug'], 'name' => $p['name'], 'price_monthly' => $p['price_monthly'],
            'trial_days' => $p['trial_days'], 'features' => null, 'is_active' => $p['is_active'],
            'created_at' => $created([]), 'updated_at' => $created([]),
        ]);
    }

    foreach ($data['users'] as $u) {
        DB::table('users')->insert([
            'id' => $u['id'], 'name' => $u['name'], 'phone' => $u['phone'], 'email' => $u['email'] ?? null,
            'password' => $hash, 'is_superadmin' => $u['is_superadmin'] ?? false,
            'blocked_at' => utc($u['blocked_at'] ?? null),
            'created_at' => $created($u), 'updated_at' => $created($u),
        ]);
    }

    foreach ($data['clubs'] as $c) {
        DB::table('clubs')->insert([
            'id' => $c['id'], 'name' => $c['name'], 'slug' => $c['slug'], 'settings' => jsonOrNull($c['settings'] ?? null),
            'locale' => $c['locale'], 'timezone' => $c['timezone'],
            'created_at' => $created($c), 'updated_at' => $created($c),
        ]);
        $s = $c['subscription'];
        DB::table('club_subscriptions')->insert([
            'club_id' => $c['id'], 'saas_plan_id' => $s['saas_plan_id'], 'status' => $s['status'],
            'started_at' => utc($s['started_at']), 'trial_ends_at' => utc($s['trial_ends_at'] ?? null),
            'current_period_ends_at' => utc($s['current_period_ends_at'] ?? null),
            'created_at' => $created($c), 'updated_at' => $created($c),
        ]);
    }

    foreach ($data['memberships'] as $m) {
        $u = collect($data['users'])->firstWhere('id', $m['user_id']);
        DB::table('club_memberships')->insert([
            'club_id' => $m['club_id'], 'user_id' => $m['user_id'], 'role' => $m['role'],
            'discount' => tenge($m['discount'] ?? null), 'total_spent' => tenge($m['total_spent'] ?? null),
            'can_manage_integrations' => $m['can_manage_integrations'] ?? false,
            'permissions' => jsonOrNull($m['permissions'] ?? null),
            'created_at' => $created($u), 'updated_at' => $created($u),
        ]);
        DB::table('member_balances')->insert([
            'club_id' => $m['club_id'], 'user_id' => $m['user_id'],
            'balance' => tenge($m['balance'] ?? null), 'bonus_balance' => tenge($m['bonus_balance'] ?? null),
            'created_at' => $created($u), 'updated_at' => $created($u),
        ]);
    }

    foreach ($data['zones'] as $z) {
        DB::table('club_zones')->insert([
            'id' => $z['id'], 'club_id' => $z['club_id'], 'name' => $z['name'], 'description' => $z['description'] ?? null,
            'capacity' => $z['capacity'] ?? null, 'icon' => $z['icon'], 'theme' => $z['theme'], 'status' => $z['status'],
            'is_featured' => $z['is_featured'] ?? false, 'sort_order' => $z['sort_order'],
            'position_x' => $z['position_x'], 'position_y' => $z['position_y'], 'width' => $z['width'], 'height' => $z['height'],
            'rotation' => $z['rotation'] ?? 0, 'price_per_hour' => $z['price_per_hour'] ?? null,
            'created_at' => $created([]), 'updated_at' => $created([]),
        ]);
    }

    foreach ($data['computers'] as $pc) {
        DB::table('club_computers')->insert([
            'id' => $pc['id'], 'club_id' => $pc['club_id'], 'club_zone_id' => $pc['zone_id'], 'number' => $pc['number'],
            'status' => $pc['status'] ?? 'available', 'is_reserved' => $pc['is_reserved'] ?? false,
            'position_x' => $pc['position_x'], 'position_y' => $pc['position_y'],
            'device_name' => $pc['device_name'] ?? null, 'display_name' => $pc['display_name'] ?? null,
            'last_seen_at' => utc($pc['last_seen_at'] ?? null), 'agent_state' => $pc['agent_state'] ?? null,
            'agent_version' => $pc['agent_version'] ?? null, 'current_session_id' => $pc['current_session_id'] ?? null,
            'current_session_user_id' => $pc['current_session_user_id'] ?? null, 'session_guest' => $pc['session_guest'] ?? false,
            'session_ends_at' => utc($pc['session_ends_at'] ?? null),
            'mac_address' => $pc['mac_address'] ?? null, 'local_ip' => $pc['local_ip'] ?? null,
            'cpu_model' => $pc['cpu_model'] ?? null, 'cpu_cores' => $pc['cpu_cores'] ?? null,
            'ram_total_mb' => $pc['ram_total_mb'] ?? null, 'gpu_model' => $pc['gpu_model'] ?? null,
            'os_version' => $pc['os_version'] ?? null, 'hardware_reported_at' => utc($pc['hardware_reported_at'] ?? null),
            'created_at' => $created([]), 'updated_at' => $created([]),
        ]);
    }

    foreach ($data['tariffs'] as $t) {
        DB::table('tariffs')->insert([
            'id' => $t['id'], 'club_id' => $t['club_id'], 'name' => $t['name'], 'description' => $t['description'] ?? null,
            'duration_minutes' => $t['duration_minutes'], 'price' => $t['price'], 'is_active' => $t['is_active'] ?? true,
            'payment_method' => $t['payment_method'], 'currency' => 'KZT', 'discount_enabled' => $t['discount_enabled'] ?? false,
            'user_roles' => jsonOrNull($t['user_roles'] ?? null),
            'usage_limit_enabled' => $t['usage_limit_enabled'] ?? false, 'usage_limit_minutes' => $t['usage_limit_minutes'] ?? null,
            'quantity_limit_enabled' => $t['quantity_limit_enabled'] ?? false, 'quantity_limit' => $t['quantity_limit'] ?? null,
            'validity_periods' => jsonOrNull($t['validity_periods'] ?? null), 'sale_periods' => jsonOrNull($t['sale_periods'] ?? null),
            'created_at' => $created([]), 'updated_at' => $created([]),
        ]);
        foreach ($t['zone_ids'] as $zoneId) {
            DB::table('club_zone_tariff')->insert(['club_zone_id' => $zoneId, 'tariff_id' => $t['id'], 'club_id' => $t['club_id']]);
        }
    }

    $categories = collect($data['product_categories'])->keyBy('id');
    foreach ($data['product_categories'] as $cat) {
        DB::table('product_categories')->insert([
            'id' => $cat['id'], 'club_id' => $cat['club_id'], 'name' => $cat['name'],
            'created_at' => $created([]), 'updated_at' => $created([]),
        ]);
    }
    foreach ($data['products'] as $p) {
        DB::table('products')->insert([
            'id' => $p['id'], 'club_id' => $p['club_id'], 'product_category_id' => $p['category_id'],
            'category' => $categories[$p['category_id']]['name'], 'sku' => $p['sku'], 'name' => $p['name'],
            'price' => $p['price'], 'quantity' => $p['quantity'],
            'created_at' => $created([]), 'updated_at' => $created([]),
        ]);
    }
    foreach ($data['services'] as $s) {
        DB::table('club_services')->insert([
            'id' => $s['id'], 'club_id' => $s['club_id'], 'name' => $s['name'], 'price' => $s['price'],
            'unit' => $s['unit'] ?? null, 'is_active' => $s['is_active'] ?? true,
            'created_at' => $created([]), 'updated_at' => $created([]),
        ]);
    }
});

// Кассовые операции — сервисом старого кода, со «сейчас» = at.
$clubs = Club::query()->get()->keyBy('id');
$cash = app(CashRegisterService::class);
foreach ($data['operations'] as $i => $op) {
    $at = Carbon::parse($op['at']);
    Carbon::setTestNow($at);
    CarbonImmutable::setTestNow($at);
    app(CurrentClub::class)->set($clubs[$op['club_id']]);
    try {
        match ($op['op']) {
            'open_shift' => $cash->open((float) $op['opening_balance'], $op['user_id']),
            'sale' => $cash->sale($op['items'], $op['user_id']),
            'top_up' => $cash->topUpBalance($op['customer_id'], (float) $op['amount'], $op['balance_type'], $op['payment_method'], $op['user_id']),
            'transaction' => $cash->transact(['type' => $op['type'], 'amount' => $op['amount'], 'description' => $op['description']], $op['user_id']),
            'close_shift' => $cash->close(CashShift::query()->where('status', CashShift::STATUS_OPEN)->firstOrFail(), $op['user_id']),
        };
    } catch (Illuminate\Validation\ValidationException $e) {
        fwrite(STDERR, "операция #{$i} ({$op['op']} {$op['at']}): ".json_encode($e->errors(), JSON_UNESCAPED_UNICODE)."\n");
        exit(1);
    }
}
Carbon::setTestNow();
CarbonImmutable::setTestNow();
app(CurrentClub::class)->clear();

DB::transaction(function () use ($data, $created): void {
    foreach ($data['shop_orders'] as $o) {
        $items = array_map(fn (array $it): array => [
            'product_id' => $it['product_id'], 'name' => $it['name'], 'quantity' => $it['quantity'],
            // Старый AgentShop::purchase(): суммы — строки formatCents в тенге.
            'price' => $it['price'], 'total' => number_format((float) $it['price'] * $it['quantity'], 2, '.', ''),
        ], $o['items']);
        DB::table('shop_orders')->insert([
            'id' => $o['id'], 'club_id' => $o['club_id'], 'user_id' => $o['user_id'], 'club_computer_id' => $o['computer_id'],
            'computer_name' => $o['computer_name'], 'items' => jsonOrNull($items), 'amount' => $o['amount'],
            'status' => $o['status'], 'handled_by' => $o['handled_by'] ?? null, 'handled_at' => utc($o['handled_at'] ?? null),
            'created_at' => $created($o), 'updated_at' => utc($o['handled_at'] ?? null) ?? $created($o),
        ]);
    }
    foreach ($data['reservations'] as $r) {
        DB::table('computer_reservations')->insert([
            'id' => $r['id'], 'club_id' => $r['club_id'], 'club_computer_id' => $r['computer_id'], 'user_id' => $r['user_id'] ?? null,
            'customer_name' => $r['customer_name'] ?? null, 'customer_phone' => $r['customer_phone'] ?? null, 'note' => $r['note'] ?? null,
            'starts_at' => utc($r['starts_at']), 'ends_at' => utc($r['ends_at']),
            'arrived_at' => utc($r['arrived_at'] ?? null), 'cancelled_at' => utc($r['cancelled_at'] ?? null),
            'created_by' => $r['created_by'] ?? null, 'created_at' => $created($r), 'updated_at' => $created($r),
        ]);
    }
    foreach ($data['news'] as $n) {
        DB::table('news')->insert([
            'id' => $n['id'], 'club_id' => $n['club_id'], 'title' => $n['title'], 'body' => $n['body'],
            'is_published' => $n['is_published'], 'created_by' => $n['created_by'] ?? null,
            'created_at' => $created($n), 'updated_at' => $created($n),
        ]);
    }
    foreach ($data['todo_tasks'] as $t) {
        DB::table('todo_tasks')->insert([
            'id' => $t['id'], 'club_id' => $t['club_id'], 'title' => $t['title'], 'due_on' => $t['due_on'],
            'created_by' => $t['created_by'] ?? null, 'done_at' => utc($t['done_at'] ?? null), 'done_by' => $t['done_by'] ?? null,
            'created_at' => $created($t), 'updated_at' => utc($t['done_at'] ?? null) ?? $created($t),
        ]);
    }
    foreach ($data['chat_messages'] as $m) {
        DB::table('chat_messages')->insert([
            'id' => $m['id'], 'club_id' => $m['club_id'], 'customer_id' => $m['customer_id'], 'author_id' => $m['author_id'] ?? null,
            'from_staff' => $m['from_staff'], 'body' => $m['body'], 'club_computer_id' => $m['computer_id'] ?? null,
            'read_at' => utc($m['read_at'] ?? null), 'created_at' => $created($m), 'updated_at' => $created($m),
        ]);
    }
    foreach ($data['activity_logs'] as $l) {
        DB::table('activity_logs')->insert([
            'club_id' => $l['club_id'], 'event' => $l['event'], 'description' => $l['description'], 'user_id' => $l['user_id'],
            'subject_type' => isset($l['subject_user_id']) ? 'App\\Models\\User' : null, 'subject_id' => $l['subject_user_id'] ?? null,
            'details' => jsonOrNull($l['details'] ?? null), 'ip_address' => '127.0.0.1', 'user_agent' => 'Zafiro visual seed',
            'created_at' => $created($l), 'updated_at' => $created($l),
        ]);
    }
});

$counts = [];
foreach (['users', 'club_computers', 'tariffs', 'products', 'cash_shifts', 'cash_transactions', 'activity_logs', 'stock_movements'] as $table) {
    $counts[$table] = DB::table($table)->count();
}
echo json_encode(['seeded' => $counts], JSON_UNESCAPED_UNICODE), "\n";
