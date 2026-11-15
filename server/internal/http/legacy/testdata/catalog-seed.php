<?php
// Данные сверки каталога (тема В): те же, что в TestCatalogMatchesLaravel нового сервера.
use Illuminate\Support\Facades\DB;
$now = '2026-10-15 07:00:00'; // «сейчас» стенда (UTC)
$club = DB::table('clubs')->insertGetId(['name' => 'Zafiro Arena Test', 'slug' => 'p3-catalog-a', 'city' => 'zhezkazgan', 'address' => 'ул. Есет батыра, 138А',
    'profile' => json_encode(['description' => 'Лучший клуб города', 'phone' => '+7 700 000 00 00', 'hours' => 'Круглосуточно', 'instagram' => 'asia.game']),
    'logo_path' => null, 'created_at' => $now, 'updated_at' => $now]);
DB::table('clubs')->where('id', $club)->update(['logo_path' => 'club-logos/'.$club.'/'.str_repeat('L', 40).'.png']);
$vip = DB::table('club_zones')->insertGetId(['club_id' => $club, 'name' => 'VIP', 'description' => 'Тихая комната', 'price_per_hour' => 800, 'sort_order' => 1,
    'devices' => json_encode(['monitor' => 'ASUS 240 Гц', 'mouse' => 'Logitech G102']), 'created_at' => $now, 'updated_at' => $now]);
$main = DB::table('club_zones')->insertGetId(['club_id' => $club, 'name' => 'Main', 'price_per_hour' => 500.5, 'sort_order' => 2, 'created_at' => $now, 'updated_at' => $now]);
$bare = DB::table('club_zones')->insertGetId(['club_id' => $club, 'name' => 'Без цены', 'price_per_hour' => null, 'sort_order' => 3, 'created_at' => $now, 'updated_at' => $now]);
DB::table('club_computers')->insert(['club_id' => $club, 'club_zone_id' => $vip, 'number' => 1, 'last_seen_at' => $now,
    'cpu_model' => '11th Gen Intel(R) Core(TM) i5-11400F @ 2.60GHz', 'gpu_model' => 'NVIDIA GeForce RTX 3060', 'ram_total_mb' => 16253, 'created_at' => $now, 'updated_at' => $now]);
DB::table('club_computers')->insert(['club_id' => $club, 'club_zone_id' => $vip, 'number' => 2, 'last_seen_at' => $now, 'is_reserved' => true, 'created_at' => $now, 'updated_at' => $now]);
DB::table('club_computers')->insert(['club_id' => $club, 'club_zone_id' => $main, 'number' => 3, 'created_at' => $now, 'updated_at' => $now]);
DB::table('club_photos')->insert(['club_id' => $club, 'path' => 'club-photos/'.$club.'/'.str_repeat('b', 40).'.jpg', 'sort_order' => 1, 'created_at' => $now, 'updated_at' => $now]);
DB::table('club_photos')->insert(['club_id' => $club, 'path' => 'club-photos/'.$club.'/'.str_repeat('a', 40).'.png', 'sort_order' => 2, 'created_at' => $now, 'updated_at' => $now]);
$second = DB::table('clubs')->insertGetId(['name' => 'Второй клуб', 'slug' => 'p3-catalog-b', 'city' => 'zhezkazgan', 'created_at' => $now, 'updated_at' => $now]);
echo json_encode(compact('club', 'second', 'vip', 'main', 'bare')), "\n";
