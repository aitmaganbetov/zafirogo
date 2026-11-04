package realtime

import (
	"strings"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
)

// SectionRights — какое право нужно, чтобы получать события раздела (первое слово типа события).
// Права — app/Support/ClubPermissions.php SECTIONS; те же, что открывают экран, который раньше опрашивал
// сервер (app/Http/Middleware/RequireClubArea.php ROUTES: club-map.state → club_map.view, chat.poll →
// chat.view, todos.state → todos.view, shop-orders.state → orders.view).
// Пустая строка — любому сотруднику клуба (колокольчик уведомлений есть у всех: ClubNotifications::for()).
// Раздела нет в таблице — событие не уходит никому (запрет по умолчанию).
var SectionRights = map[string]string{
	"notifications": "",
	"computer":      "club_map.view",
	"reservation":   "reservations.view",
	"chat":          "chat.view",
	"todo":          "todos.view",
	"shop_order":    "orders.view",
	"cash":          "cash.view",
	"sale":          "cash.view",
	"balance":       "customers.view",
	"product":       "warehouse.view",
	"stock":         "stock.view",
}

// Allowed — может ли сотрудник p получить событие типа eventType. Нужна роль в выбранном клубе;
// суперадмин сам по себе прав в клубе не имеет — в режиме просмотра клуба Principal собирается как
// владелец (auth/principal, Can).
func Allowed(p principal.Principal, eventType string) bool {
	section, _, _ := strings.Cut(eventType, ".")
	right, ok := SectionRights[section]
	if !ok {
		return false
	}
	if p.ClubID <= 0 || p.Role == "" {
		return false
	}
	if right == "" {
		return true
	}
	return p.Can(right)
}
