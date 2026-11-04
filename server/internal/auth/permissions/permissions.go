// Package permissions — роли и права панели клуба: буквальный перенос
// app/Support/ClubPermissions.php, ClubRoles.php, StaffPermissions.php и таблицы «маршрут → право»
// app/Http/Middleware/RequireClubArea.php (BR-140…BR-144), плюс HTTP-middleware поверх auth/principal.
package permissions

import (
	"encoding/json"
	"slices"
	"strings"
)

// Действия. Laravel: ClubPermissions::ACTIONS.
const (
	ActionView   = "view"
	ActionCreate = "create"
	ActionUpdate = "update"
	ActionDelete = "delete"
)

// Actions — порядок столбцов на странице «Права». Laravel: ClubPermissions::ACTIONS.
var Actions = []string{ActionView, ActionCreate, ActionUpdate, ActionDelete}

// Группы ролей. Laravel: ClubPermissions::STAFF, DESK, MANAGERS (private const).
var (
	staff    = []string{RoleAdmin, RoleManager, RoleCashier, RoleOperator}
	desk     = []string{RoleAdmin, RoleManager, RoleCashier}
	managers = []string{RoleAdmin, RoleManager}
)

// Section — раздел и роли, у которых действие есть по умолчанию.
type Section struct {
	Name string
	// Actions — действие → роли по умолчанию. Действие, которого нет в карте, не существует;
	// пустой список — право есть только у владельца (или по выбору для сотрудника).
	Actions map[string][]string
}

// Sections — Laravel: ClubPermissions::SECTIONS (стр. 24–45), в том же порядке.
var Sections = []Section{
	{"cash", map[string][]string{ActionView: desk, ActionCreate: desk, ActionDelete: managers}},
	{"customers", map[string][]string{ActionView: staff, ActionCreate: staff, ActionUpdate: staff}},
	{"customer_money", map[string][]string{ActionUpdate: managers}},
	{"reservations", map[string][]string{ActionView: staff, ActionCreate: staff, ActionUpdate: staff, ActionDelete: staff}},
	{"club_map", map[string][]string{ActionView: staff, ActionCreate: managers, ActionUpdate: managers, ActionDelete: managers}},
	// Ending a customer's session: as in the staff app (ClubComputerSessions::STAFF_ROLES).
	{"pc_control", map[string][]string{ActionUpdate: staff, ActionDelete: {RoleAdmin, RoleManager, RoleOperator}}},
	{"tariffs", map[string][]string{ActionView: managers, ActionCreate: managers, ActionUpdate: managers, ActionDelete: {}}},
	{"warehouse", map[string][]string{ActionView: managers, ActionCreate: managers, ActionUpdate: managers, ActionDelete: {}}},
	{"services", map[string][]string{ActionView: managers, ActionCreate: managers, ActionUpdate: managers, ActionDelete: managers}},
	{"stock", map[string][]string{ActionView: staff, ActionCreate: staff}},
	{"orders", map[string][]string{ActionView: staff, ActionUpdate: staff, ActionDelete: staff}},
	// Customers are answered by the front desk (ChatMessage::ANSWERING_ROLES); others read.
	{"chat", map[string][]string{ActionView: staff, ActionCreate: {RoleCashier, RoleOperator}}},
	{"todos", map[string][]string{ActionView: staff, ActionCreate: managers, ActionUpdate: staff, ActionDelete: managers}},
	{"news", map[string][]string{ActionView: managers, ActionCreate: managers, ActionUpdate: managers, ActionDelete: managers}},
	{"activity_logs", map[string][]string{ActionView: managers}},
	{"settings", map[string][]string{ActionView: managers, ActionUpdate: managers}},
	{"workers", map[string][]string{ActionView: {RoleAdmin}, ActionCreate: {RoleAdmin}, ActionUpdate: {RoleAdmin}, ActionDelete: {RoleAdmin}}},
	{"billing", map[string][]string{ActionView: managers}},
}

var sectionIndex = func() map[string]map[string][]string {
	m := make(map[string]map[string][]string, len(Sections))
	for _, s := range Sections {
		m[s.Name] = s.Actions
	}
	return m
}()

// Membership — то, что нужно правам из club_memberships: роль и выбор владельца для сотрудника.
type Membership struct {
	Role string
	// Given — club_memberships.permissions: {"section.action": true|false}. Значения не bool не учитываются.
	Given map[string]bool
}

// ParseGiven разбирает club_memberships.permissions (jsonb). Как ClubPermissions::given(): берутся только
// значения bool (is_bool), прочее — «как у роли». Пустое/NULL/не объект — нет выбора.
func ParseGiven(raw []byte) map[string]bool {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	out := make(map[string]bool, len(m))
	for k, v := range m {
		switch strings.TrimSpace(string(v)) {
		case "true":
			out[k] = true
		case "false":
			out[k] = false
		}
	}
	return out
}

func split(right string) (string, string) {
	// Laravel: array_pad(explode('.', $right, 2), 2, '')
	section, action, _ := strings.Cut(right, ".")
	return section, action
}

// Exists — Laravel: ClubPermissions::exists().
func Exists(right string) bool {
	section, action := split(right)
	_, ok := sectionIndex[section][action]
	return ok
}

// ByRole — право по роли без выбора владельца. Laravel: ClubPermissions::byRole() (стр. 67–72).
// Как в PHP, наличие права не проверяется: несуществующее право у владельца — true.
func ByRole(role, right string) bool {
	section, action := split(right)
	return role == RoleOwner || slices.Contains(sectionIndex[section][action], role)
}

// Given — выбор владельца для сотрудника; ok = false — как у роли. Laravel: ClubPermissions::given().
func Given(m *Membership, right string) (value, ok bool) {
	if m == nil {
		return false, false
	}
	value, ok = m.Given[right]
	return value, ok
}

// Allows — есть ли у члена клуба право. Laravel: ClubPermissions::allows() (стр. 48–58), BR-140:
//
//	membership == null || !exists(right) → false; role == owner → true; given(m, right) ?? byRole(role, right)
func Allows(m *Membership, right string) bool {
	if m == nil || !Exists(right) {
		return false
	}
	if m.Role == RoleOwner {
		return true
	}
	if v, ok := Given(m, right); ok {
		return v
	}
	return ByRole(m.Role, right)
}

// All — все права «section.action» в порядке SECTIONS. Laravel: ClubPermissions::all().
func All() []string {
	rights := make([]string, 0, 64)
	for _, s := range Sections {
		for _, a := range Actions { // порядок действий как в литерале PHP (view, create, update, delete)
			if _, ok := s.Actions[a]; ok {
				rights = append(rights, s.Name+"."+a)
			}
		}
	}
	return rights
}

// For — все права члена клуба (для Principal.Permissions и ответа /api/auth/me).
func For(m *Membership) []string {
	if m == nil {
		return []string{}
	}
	out := make([]string, 0, 64)
	for _, r := range All() {
		if Allows(m, r) {
			out = append(out, r)
		}
	}
	return out
}

// ActionLabels — заголовки столбцов (ключи перевода). Laravel: ClubPermissions::actionLabels().
var ActionLabels = map[string]string{
	ActionView: "Просмотр", ActionCreate: "Создание", ActionUpdate: "Изменение", ActionDelete: "Удаление",
}

// SectionLabel — подписи раздела и его действий (ключи перевода). Laravel: ClubPermissions::sections().
type SectionLabel struct {
	Label   string
	Actions map[string]string
}

// SectionLabels — Laravel: ClubPermissions::sections().
var SectionLabels = map[string]SectionLabel{
	"cash":           {"Касса", map[string]string{"view": "Касса, чеки, X/Z-отчёты", "create": "Смены, продажи, пополнения, приход и расход, тариф гостю", "delete": "Возвраты"}},
	"customers":      {"Клиенты", map[string]string{"view": "Список клиентов", "create": "Добавить клиента", "update": "Изменить клиента, сбросить пароль"}},
	"customer_money": {"Скидка и баланс клиента", map[string]string{"update": "Персональная скидка и начальный баланс"}},
	"reservations":   {"Брони", map[string]string{"view": "Список броней", "create": "Забронировать ПК", "update": "Отметить приход", "delete": "Отменить бронь"}},
	"club_map":       {"Карта клуба и ПК", map[string]string{"view": "Карта и список ПК", "create": "Добавить зону или ПК", "update": "Изменить зоны, ПК, расстановку", "delete": "Удалить зону или ПК"}},
	"pc_control":     {"Управление ПК", map[string]string{"update": "Включить, выключить, перезагрузить, забронировать, техрежим", "delete": "Завершить сессию клиента"}},
	"tariffs":        {"Тарифы", map[string]string{"view": "Список тарифов", "create": "Добавить тариф", "update": "Изменить тариф", "delete": "Удалить тариф"}},
	"warehouse":      {"Склад: каталог", map[string]string{"view": "Каталог товаров", "create": "Добавить товар или категорию", "update": "Изменить товар, списать или оприходовать", "delete": "Удалить товар"}},
	"services":       {"Услуги", map[string]string{"view": "Список услуг", "create": "Добавить услугу", "update": "Изменить цену и название", "delete": "Удалить услугу"}},
	"stock":          {"Приход товара", map[string]string{"view": "Остатки на складе", "create": "Принять товар"}},
	"orders":         {"Заказы из агента", map[string]string{"view": "Список заказов", "update": "Отметить «Выдан»", "delete": "Отменить заказ"}},
	"chat":           {"Чат с клиентами", map[string]string{"view": "Читать чат", "create": "Отвечать"}},
	"todos":          {"To Do List", map[string]string{"view": "Список задач", "create": "Добавить задачу", "update": "Отметить выполнение", "delete": "Удалить задачу"}},
	"news":           {"Новости", map[string]string{"view": "Список новостей", "create": "Добавить новость", "update": "Изменить новость", "delete": "Удалить новость"}},
	"activity_logs":  {"Журнал действий", map[string]string{"view": "Логи клуба"}},
	"settings":       {"Настройки клуба", map[string]string{"view": "Открыть настройки", "update": "Сохранить настройки, логотип, картинку"}},
	"workers":        {"Работники", map[string]string{"view": "Список сотрудников", "create": "Добавить или пригласить", "update": "Изменить роль, PIN-код агента", "delete": "Убрать сотрудника"}},
	"billing":        {"Подписка", map[string]string{"view": "Тариф Zafiro и оплата"}},
}
