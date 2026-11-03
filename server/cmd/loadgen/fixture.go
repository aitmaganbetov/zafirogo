//go:build loadgen

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"
)

// Раскладка идентификаторов нагрузочного набора (не пересекается с демо-данными tools/visual/fixtures/demo.json,
// у которых id < 100): клуб c = 1..N, ПК i = 1..pcs.
//
//	пользователи: c*10000 + 1 (владелец), + 2 (кассир), + 100 + i (гость ПК i)
//	ПК:           c*10000 + i
//	зоны, тарифы, товары: c*10 + k
const (
	ownerK   = 1
	cashierK = 2
	guestK   = 100
	// Пароль всех пользователей набора; хэш bcrypt — тот же, что у демо-данных (login.password_bcrypt), prepare
	// заменяет его на Argon2id, чтобы первый вход каждого гостя не стоил bcrypt cost 12 + перехеширование.
	loadPassword       = "zafiro-demo-2026"                                             // #nosec G101 -- демо-пароль staging (tools/visual/fixtures/demo.json)
	loadPasswordBcrypt = "$2y$12$kcsi9e6mjtXK5IJ/J9FThusgS6dx185nlo1hBjj48QOnOJKDT2UUm" // #nosec G101 -- его bcrypt из демо-данных
	// Начальный баланс гостя, тенге (тиыны — ×100).
	guestBalanceKZT = 100000
)

func userID(club, k int) int64       { return int64(club*10000 + k) }
func guestUserID(club, pc int) int64 { return userID(club, guestK+pc) }
func computerID(club, pc int) int64  { return int64(club*10000 + pc) }
func zoneID(club, k int) int64       { return int64(club*10 + k) }
func phone(club, k int) string       { return fmt.Sprintf("+7702%03d%04d", club, k) }
func guestPhone(club, pc int) string { return phone(club, guestK+pc) }

// autoBillingClub — клубы с автобиллингом (чётные): гости, которые вошли и не купили время, покупают автоматически.
func autoBillingClub(club int) bool { return club%2 == 0 }

func cmdFixture(args []string) error {
	fs := flag.NewFlagSet("fixture", flag.ExitOnError)
	clubs := fs.Int("clubs", 50, "клубов")
	pcs := fs.Int("pcs", 100, "ПК в клубе")
	out := fs.String("out", "", "куда записать JSON (формат tools/visual/fixtures/demo.json)")
	_ = fs.Parse(args)
	if *out == "" || *clubs < 1 || *pcs < 1 || *pcs > 9000 || *clubs > 999 {
		return fmt.Errorf("fixture: -out обязателен, 1 ≤ clubs ≤ 999, 1 ≤ pcs ≤ 9000")
	}
	almaty, err := time.LoadLocation("Asia/Almaty")
	if err != nil {
		return err
	}
	now := time.Now().In(almaty).Truncate(time.Second)
	at := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	type m = map[string]any
	f := m{
		"_comment": "Нагрузочный набор O4 (server/cmd/loadgen fixture). Не для визуального сравнения.",
		"now":      at(0),
		"login":    m{"password": loadPassword, "password_bcrypt": loadPasswordBcrypt, "roles": m{}},
		"saas_plans": []m{{"id": 1, "slug": "pro", "name": "Профессиональный", "price_monthly": "25000.00",
			"trial_days": 0, "is_active": true}},
	}
	var clubsL, users, members, zones, computers, tariffs, cats, products, ops []m
	for c := 1; c <= *clubs; c++ {
		clubsL = append(clubsL, m{
			"id": c, "name": fmt.Sprintf("Load Club %03d", c), "slug": fmt.Sprintf("load-%03d", c),
			"timezone": "Asia/Almaty", "locale": "ru", "created_at": at(-30 * 24 * time.Hour),
			"settings": m{"finances": m{"auto_billing": autoBillingClub(c), "auto_billing_pause_minutes": 1}},
			"subscription": m{"saas_plan_id": 1, "status": "active", "started_at": at(-24 * time.Hour),
				"current_period_ends_at": at(30 * 24 * time.Hour)},
		})
		created := at(-7 * 24 * time.Hour)
		users = append(users,
			m{"id": userID(c, ownerK), "name": fmt.Sprintf("Владелец %03d", c), "phone": phone(c, ownerK), "created_at": created},
			m{"id": userID(c, cashierK), "name": fmt.Sprintf("Кассир %03d", c), "phone": phone(c, cashierK), "created_at": created})
		members = append(members,
			m{"club_id": c, "user_id": userID(c, ownerK), "role": "owner", "can_manage_integrations": true},
			m{"club_id": c, "user_id": userID(c, cashierK), "role": "cashier"})
		zones = append(zones,
			m{"id": zoneID(c, 1), "club_id": c, "name": "Основной зал", "capacity": *pcs, "icon": "desktop", "theme": "neutral",
				"status": "available", "sort_order": 10, "position_x": 20, "position_y": 20, "width": 900, "height": 600, "price_per_hour": "600.00"},
			m{"id": zoneID(c, 2), "club_id": c, "name": "VIP-зал", "capacity": *pcs, "icon": "crown", "theme": "green",
				"status": "available", "sort_order": 20, "position_x": 940, "position_y": 20, "width": 400, "height": 600, "price_per_hour": "1000.00"})
		for i := 1; i <= *pcs; i++ {
			zone := zoneID(c, 1)
			if i%5 == 0 {
				zone = zoneID(c, 2)
			}
			users = append(users, m{"id": guestUserID(c, i), "name": fmt.Sprintf("Гость %03d-%04d", c, i), "phone": guestPhone(c, i), "created_at": created})
			members = append(members, m{"club_id": c, "user_id": guestUserID(c, i), "role": "client", "balance": fmt.Sprintf("%d.00", guestBalanceKZT),
				"bonus_balance": "1000.00"})
			computers = append(computers, m{"id": computerID(c, i), "club_id": c, "zone_id": zone, "number": i, "status": "offline",
				"position_x": (i % 20) * 40, "position_y": (i / 20) * 40, "device_name": fmt.Sprintf("PC-%03d", i),
				"mac_address": fmt.Sprintf("02:4C:%02X:%02X:%02X:%02X", c>>8, c&0xff, i>>8, i&0xff)})
		}
		both := []int64{zoneID(c, 1), zoneID(c, 2)}
		tariffs = append(tariffs,
			m{"id": zoneID(c, 1), "club_id": c, "name": "1 час", "duration_minutes": 60, "price": "600.00", "payment_method": "main_or_bonus", "zone_ids": both},
			m{"id": zoneID(c, 2), "club_id": c, "name": "3 часа", "duration_minutes": 180, "price": "1500.00", "payment_method": "main_or_bonus", "zone_ids": both})
		cats = append(cats, m{"id": zoneID(c, 1), "club_id": c, "name": "Напитки"})
		for k, p := range []struct {
			name, price string
		}{{"Coca-Cola 0,5 л", "450.00"}, {"Red Bull 0,25 л", "900.00"}, {"Вода 0,5 л", "250.00"}} {
			products = append(products, m{"id": zoneID(c, k+1), "club_id": c, "category_id": zoneID(c, 1), "sku": fmt.Sprintf("LD-%03d-%d", c, k+1),
				"name": p.name, "price": p.price, "quantity": 1000000})
		}
		ops = append(ops, m{"at": at(-time.Hour), "op": "open_shift", "club_id": c, "user_id": userID(c, cashierK), "opening_balance": "10000.00"})
	}
	f["clubs"], f["users"], f["memberships"], f["zones"], f["computers"] = clubsL, users, members, zones, computers
	f["tariffs"], f["product_categories"], f["products"], f["operations"] = tariffs, cats, products, ops
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, b, 0o600); err != nil {
		return err
	}
	fmt.Printf("fixture: %d клубов × %d ПК, %d пользователей → %s\n", *clubs, *pcs, len(users), *out)
	return nil
}
