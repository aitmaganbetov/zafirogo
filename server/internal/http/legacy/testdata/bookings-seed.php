<?php
// Данные сверки броней из приложения (тема Г): те же, что в TestBookingsMatchLaravel нового сервера.
// Клуб — «legacy» из миграций старого проекта (подписка активна), пояс Asia/Almaty, «сейчас» стенда —
// понедельник 2026-11-16 10:00 Алматы (05:00 UTC): storage/framework/visual-now = 2026-11-16T05:00:00+00:00.
use App\Models\Club;
use App\Models\ClubComputer;
use App\Models\ClubMembership;
use App\Models\ClubZone;
use App\Models\MemberBalance;
use App\Models\Tariff;
use App\Models\User;
use App\Services\CurrentClub;
use Illuminate\Support\Carbon;
use Illuminate\Support\Facades\Hash;

Carbon::setTestNow(Carbon::parse('2026-11-16T05:00:00+00:00'));
$club = Club::query()->where('slug', 'legacy')->firstOrFail();
$club->forceFill(['timezone' => 'Asia/Almaty', 'settings' => [
    'club' => ['booking_interval' => 10, 'app_booking_enabled' => true],
    'finances' => ['currency' => 'KZT', 'allow_mixed_payment' => true, 'bonus_max_percent' => 50],
]])->save();
app(CurrentClub::class)->set($club);

$vip = ClubZone::query()->create(['name' => 'VIP', 'price_per_hour' => '600.00', 'sort_order' => 1]);
$common = ClubZone::query()->create(['name' => 'Общий', 'price_per_hour' => '333.33', 'sort_order' => 2]);
$bare = ClubZone::query()->create(['name' => 'Без цены', 'price_per_hour' => null, 'sort_order' => 3]);
$pc1 = ClubComputer::query()->create(['club_zone_id' => $vip->id, 'number' => 1]);
$pc2 = ClubComputer::query()->create(['club_zone_id' => $vip->id, 'number' => 2]);
$pc3 = ClubComputer::query()->create(['club_zone_id' => $common->id, 'number' => 3]);
$pc4 = ClubComputer::query()->create(['club_zone_id' => $common->id, 'number' => 4, 'is_reserved' => true]);
$pc5 = ClubComputer::query()->create(['club_zone_id' => $bare->id, 'number' => 5, 'display_name' => 'Стрим']);

$three = Tariff::query()->create(['name' => '3 часа', 'duration_minutes' => 180, 'price' => '1200.00', 'payment_method' => 'main_or_bonus',
    'quantity_limit_enabled' => true, 'quantity_limit' => 3, 'is_active' => true]);
$three->zones()->sync([$vip->id => ['club_id' => $club->id]]);
$night = Tariff::query()->create(['name' => 'Ночь', 'description' => 'Ночной пакет', 'duration_minutes' => 600, 'price' => '2000.00',
    'payment_method' => 'main_or_bonus', 'is_active' => true,
    'validity_periods' => [['start_day' => 'mon', 'start_time' => '22:00', 'end_day' => 'tue', 'end_time' => '08:00']]]);
$hour = Tariff::query()->create(['name' => 'Час', 'duration_minutes' => 60, 'price' => '300.00', 'payment_method' => 'main_or_bonus',
    'discount_enabled' => true, 'is_active' => true]);

$guest = User::query()->create(['name' => 'Гость Тест', 'phone' => '+77010000001', 'password' => Hash::make('pw')]);
ClubMembership::query()->firstOrCreate(['user_id' => $guest->id], ['role' => 'client']);
ClubMembership::query()->where('user_id', $guest->id)->update(['discount' => 10]);
MemberBalance::query()->updateOrCreate(['user_id' => $guest->id], ['balance' => '5000.00', 'bonus_balance' => '1000.00']);
$poor = User::query()->create(['name' => 'Гость Без Денег', 'phone' => '+77010000002', 'password' => Hash::make('pw')]);
ClubMembership::query()->firstOrCreate(['user_id' => $poor->id], ['role' => 'client']);
MemberBalance::query()->updateOrCreate(['user_id' => $poor->id], ['balance' => '100.00', 'bonus_balance' => '0.00']);

echo json_encode([
    'club' => $club->id, 'vip' => $vip->id, 'common' => $common->id, 'bare' => $bare->id,
    'pc1' => $pc1->id, 'pc2' => $pc2->id, 'pc3' => $pc3->id, 'pc4' => $pc4->id, 'pc5' => $pc5->id,
    'three' => $three->id, 'night' => $night->id, 'hour' => $hour->id,
    'guest' => $guest->id, 'poor' => $poor->id,
    'token' => $guest->createToken('mobile', ['club:'.$club->id])->plainTextToken,
    'poor_token' => $poor->createToken('mobile', ['club:'.$club->id])->plainTextToken,
]), "\n";
