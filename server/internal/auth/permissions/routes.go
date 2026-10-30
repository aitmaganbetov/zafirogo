package permissions

// RouteRights — право, которое нужно маршруту панели (по имени маршрута Laravel).
// Laravel: app/Http/Middleware/RequireClubArea.php ROUTES (стр. 18–70), BR-144.
// Новые эндпоинты админки (волна B) берут право отсюда по имени старого маршрута: так таблица
// «маршрут → право» остаётся одной, а тест полноты (GS-254) проверяет, что каждое право существует.
var RouteRights = []struct {
	Right  string
	Routes []string
}{
	{"cash.view", []string{"cash-register.index", "cash-register.operations", "cash-register.receipt", "cash-register.x-report", "cash-register.z-report"}},
	{"cash.create", []string{"cash-register.shifts.open", "cash-register.sales.store", "cash-register.balance-top-ups.store", "cash-register.transactions.store", "cash-register.shifts.close", "cash-register.z-report.printed", "club-computers.guest-session"}},
	{"cash.delete", []string{"cash-register.refund"}},
	{"customers.view", []string{"users.index"}},
	{"customers.create", []string{"users.store"}},
	{"customers.update", []string{"users.update", "users.password"}},
	{"reservations.view", []string{"reservations.index", "reservations.customers"}},
	{"reservations.create", []string{"reservations.store", "reservations.store-many"}},
	{"reservations.update", []string{"reservations.arrive"}},
	{"reservations.delete", []string{"reservations.cancel"}},
	{"club_map.view", []string{"club-map.index", "club-map.state", "club-computers.index"}},
	{"club_map.create", []string{"club-map.store", "club-computers.store"}},
	{"club_map.update", []string{"club-map.update", "club-map.layout", "club-map.align", "club-computers.update", "club-computers.layout"}},
	{"club_map.delete", []string{"club-map.destroy", "club-computers.destroy"}},
	{"pc_control.update", []string{"club-computers.control", "club-computers.notify"}},
	{"pc_control.delete", []string{"club-computers.end-sessions"}},
	{"tariffs.view", []string{"tariffs.index", "tariffs.edit"}},
	{"tariffs.create", []string{"tariffs.store"}},
	{"tariffs.update", []string{"tariffs.update"}},
	{"tariffs.delete", []string{"tariffs.destroy"}},
	{"warehouse.view", []string{"warehouse"}},
	{"warehouse.create", []string{"warehouse.products.store", "warehouse.categories.store"}},
	{"warehouse.update", []string{"warehouse.products.update", "warehouse.stock-movements.store"}},
	{"warehouse.delete", []string{"warehouse.products.destroy"}},
	{"services.view", []string{"services.index"}},
	{"services.create", []string{"services.store"}},
	{"services.update", []string{"services.update"}},
	{"services.delete", []string{"services.destroy"}},
	{"stock.view", []string{"stock.index"}},
	{"stock.create", []string{"stock.receive"}},
	{"orders.view", []string{"shop-orders.index", "shop-orders.state"}},
	{"orders.update", []string{"shop-orders.done"}},
	{"orders.delete", []string{"shop-orders.cancel"}},
	{"chat.view", []string{"chat.index", "chat.poll"}},
	{"chat.create", []string{"chat.reply"}},
	{"todos.view", []string{"todos.index", "todos.state"}},
	{"todos.create", []string{"todos.store"}},
	{"todos.update", []string{"todos.toggle"}},
	{"todos.delete", []string{"todos.destroy"}},
	{"news.view", []string{"news.index"}},
	{"news.create", []string{"news.store"}},
	{"news.update", []string{"news.update"}},
	{"news.delete", []string{"news.destroy"}},
	{"activity_logs.view", []string{"activity-logs.index"}},
	{"settings.view", []string{"settings", "settings.club", "settings.languages", "settings.finances", "settings.guests", "settings.api"}},
	{"settings.update", []string{"settings.save", "settings.language", "settings.timezone", "settings.logo", "settings.logo.destroy", "settings.signin-image", "settings.signin-image.destroy"}},
	{"workers.view", []string{"workers.index"}},
	{"workers.create", []string{"workers.store", "club-invitations.store"}},
	{"workers.update", []string{"workers.update", "settings.agent-pin", "settings.agent-staff-time"}},
	{"workers.delete", []string{"workers.destroy"}},
	{"billing.view", []string{"billing.show"}},
}

// RightFor — право маршрута; "" — маршрута нет в таблице (такой маршрут запрещён). Laravel: RequireClubArea::rightFor().
func RightFor(route string) string {
	for _, rr := range RouteRights {
		for _, name := range rr.Routes {
			if name == route {
				return rr.Right
			}
		}
	}
	return ""
}
