package permissions

import "slices"

// Роли членов клуба. Laravel: app/Support/ClubRoles.php (BR-141).
const (
	RoleOwner    = "owner"
	RoleAdmin    = "admin"
	RoleManager  = "manager"
	RoleCashier  = "cashier"
	RoleOperator = "operator"
	// RoleClient — роль новых клиентов. Laravel: ClubRoles::CLIENT. У старых клиентов бывают
	// другие не-сотруднические роли (например «Пользователь») — они тоже клиенты.
	RoleClient = "client"
)

// StaffRoles — сотрудники, работают в панели. Laravel: ClubRoles::STAFF.
var StaffRoles = []string{RoleOwner, RoleAdmin, RoleManager, RoleCashier, RoleOperator}

// ShiftRoles — роли «на смене», число которых клуб может ограничить. Laravel: ClubRoles::SHIFT (BR-060).
var ShiftRoles = []string{RoleCashier, RoleOperator}

// ComputerStaffRoles — кто завершает сессию на ПК из агента/приложения (кассир — нет).
// Laravel: app/Services/ClubComputerSessions.php STAFF_ROLES.
var ComputerStaffRoles = []string{RoleOwner, RoleAdmin, RoleManager, RoleOperator}

// ReportRoles — отчёт дня в приложении сотрудника. Laravel: StaffPermissions::REPORTS.
var ReportRoles = []string{RoleOwner, RoleAdmin, RoleManager}

// ChatAnsweringRoles — Laravel: ChatMessage::ANSWERING_ROLES (в проверке не используется: отвечает право chat.create).
var ChatAnsweringRoles = []string{RoleCashier, RoleOperator}

// IsStaff — Laravel: ClubRoles::isStaff().
func IsStaff(role string) bool { return slices.Contains(StaffRoles, role) }

// IsShiftRole — роль из ClubRoles::SHIFT.
func IsShiftRole(role string) bool { return slices.Contains(ShiftRoles, role) }

// AssignableRoles — роли, которые клуб назначает сотрудникам (ключи перевода подписей).
// Laravel: ClubRoles::assignable(), порядок сохранён.
var AssignableRoles = []struct{ Role, Label string }{
	{RoleAdmin, "Администратор"},
	{RoleManager, "Менеджер"},
	{RoleCashier, "Кассир"},
	{RoleOperator, "Оператор"},
}

// RoleLabel — подпись роли (ключ перевода). Laravel: ClubRoles::label(); любая другая роль — «Клиент».
func RoleLabel(role string) string {
	switch role {
	case RoleOwner:
		return "Владелец"
	case RoleAdmin:
		return "Администратор"
	case RoleManager:
		return "Менеджер"
	case RoleCashier:
		return "Кассир"
	case RoleOperator:
		return "Оператор"
	}
	return "Клиент"
}

// StaffApp — права приложения сотрудника. Laravel: app/Support/StaffPermissions.php for() (BR-142).
type StaffApp struct {
	Cash         bool `json:"cash"`
	GuestSession bool `json:"guest_session"`
	EndSession   bool `json:"end_session"`
	Control      bool `json:"control"`
	ChatReply    bool `json:"chat_reply"`
	StockReceive bool `json:"stock_receive"`
	StockManage  bool `json:"stock_manage"`
	Reports      bool `json:"reports"`
}

// StaffAppFor — Laravel: StaffPermissions::for($membership, $guestMode).
func StaffAppFor(m *Membership, guestMode bool) StaffApp {
	role := ""
	if m != nil {
		role = m.Role
	}
	return StaffApp{
		Cash:         Allows(m, "cash.view") && Allows(m, "cash.create"),
		GuestSession: guestMode && Allows(m, "cash.create"),
		EndSession:   m != nil && slices.Contains(ComputerStaffRoles, role),
		Control:      Allows(m, "pc_control.update"),
		ChatReply:    Allows(m, "chat.create"),
		StockReceive: Allows(m, "stock.create"),
		StockManage:  Allows(m, "warehouse.update"),
		Reports:      m != nil && slices.Contains(ReportRoles, role),
	}
}
