package permissions

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
)

// matrix — BR-140 «Полная матрица по умолчанию» (docs/business-rules.md), набрана независимо от Sections:
// право → роли (кроме владельца), у которых оно есть. Порядок — ClubPermissions::all().
var matrix = []struct {
	right string
	roles string // a — admin, m — manager, c — cashier, o — operator
}{
	{"cash.view", "amc"}, {"cash.create", "amc"}, {"cash.delete", "am"},
	{"customers.view", "amco"}, {"customers.create", "amco"}, {"customers.update", "amco"},
	{"customer_money.update", "am"},
	{"reservations.view", "amco"}, {"reservations.create", "amco"}, {"reservations.update", "amco"}, {"reservations.delete", "amco"},
	{"club_map.view", "amco"}, {"club_map.create", "am"}, {"club_map.update", "am"}, {"club_map.delete", "am"},
	{"pc_control.update", "amco"}, {"pc_control.delete", "amo"},
	{"tariffs.view", "am"}, {"tariffs.create", "am"}, {"tariffs.update", "am"}, {"tariffs.delete", ""},
	{"warehouse.view", "am"}, {"warehouse.create", "am"}, {"warehouse.update", "am"}, {"warehouse.delete", ""},
	{"services.view", "am"}, {"services.create", "am"}, {"services.update", "am"}, {"services.delete", "am"},
	{"stock.view", "amco"}, {"stock.create", "amco"},
	{"orders.view", "amco"}, {"orders.update", "amco"}, {"orders.delete", "amco"},
	{"chat.view", "amco"}, {"chat.create", "co"},
	{"todos.view", "amco"}, {"todos.create", "am"}, {"todos.update", "amco"}, {"todos.delete", "am"},
	{"news.view", "am"}, {"news.create", "am"}, {"news.update", "am"}, {"news.delete", "am"},
	{"activity_logs.view", "am"},
	{"settings.view", "am"}, {"settings.update", "am"},
	{"workers.view", "a"}, {"workers.create", "a"}, {"workers.update", "a"}, {"workers.delete", "a"},
	{"billing.view", "am"},
}

var roleLetter = map[string]byte{RoleAdmin: 'a', RoleManager: 'm', RoleCashier: 'c', RoleOperator: 'o'}

func TestMatrix(t *testing.T) {
	var rights []string
	for _, row := range matrix {
		rights = append(rights, row.right)
	}
	if got := All(); !slices.Equal(got, rights) {
		t.Fatalf("All() = %v\nwant %v", got, rights)
	}
	for _, row := range matrix {
		t.Run("BR-140/"+row.right, func(t *testing.T) {
			if !Allows(&Membership{Role: RoleOwner}, row.right) {
				t.Error("owner must have every right")
			}
			for role, letter := range roleLetter {
				want := strings.IndexByte(row.roles, letter) >= 0
				if got := Allows(&Membership{Role: role}, row.right); got != want {
					t.Errorf("%s: %v, want %v", role, got, want)
				}
			}
			for _, role := range []string{RoleClient, "Пользователь", "Гость", ""} {
				if Allows(&Membership{Role: role}, row.right) {
					t.Errorf("%q must not have %s", role, row.right)
				}
			}
		})
	}
}

func TestAllowsEdges(t *testing.T) {
	cases := []struct {
		name  string
		m     *Membership
		right string
		want  bool
	}{
		{"nil membership", nil, "cash.view", false},
		{"unknown right owner", &Membership{Role: RoleOwner}, "cash.update", false},
		{"unknown section", &Membership{Role: RoleOwner}, "nope.view", false},
		{"no action", &Membership{Role: RoleOwner}, "cash", false},
		{"extra dot", &Membership{Role: RoleOwner}, "cash.view.x", false},
		// Владельцу выбор не применяется (allows: owner → true до given()).
		{"owner given false", &Membership{Role: RoleOwner, Given: map[string]bool{"cash.view": false}}, "cash.view", true},
		// Выбор для сотрудника сильнее роли — в обе стороны.
		{"cashier given true", &Membership{Role: RoleCashier, Given: map[string]bool{"tariffs.delete": true}}, "tariffs.delete", true},
		{"admin given false", &Membership{Role: RoleAdmin, Given: map[string]bool{"workers.view": false}}, "workers.view", false},
		// Выбор для клиента тоже учитывается (allows не проверяет, что роль — сотрудник).
		{"client given true", &Membership{Role: RoleClient, Given: map[string]bool{"chat.view": true}}, "chat.view", true},
		// Выбор для несуществующего права не делает его существующим.
		{"given unknown", &Membership{Role: RoleCashier, Given: map[string]bool{"cash.update": true}}, "cash.update", false},
	}
	for _, c := range cases {
		if got := Allows(c.m, c.right); got != c.want {
			t.Errorf("%s: Allows(%s) = %v, want %v", c.name, c.right, got, c.want)
		}
	}
	// byRole не проверяет существование права (как PHP).
	if !ByRole(RoleOwner, "nope.view") || ByRole(RoleAdmin, "nope.view") {
		t.Error("ByRole")
	}
}

func TestParseGiven(t *testing.T) {
	got := ParseGiven([]byte(`{"cash.view": false, "warehouse.view": true, "x.y": 1, "z.w": "true", "n.v": null}`))
	want := map[string]bool{"cash.view": false, "warehouse.view": true}
	if len(got) != len(want) || got["cash.view"] || !got["warehouse.view"] {
		t.Fatalf("ParseGiven = %v", got)
	}
	for _, raw := range []string{"", "null", "[]", "[true]", "garbage"} {
		if g := ParseGiven([]byte(raw)); len(g) != 0 {
			t.Errorf("%q → %v", raw, g)
		}
	}
}

// GS-249 · PermissionsTest «by default every role keeps the rights it had».
func TestGS249_RoleDefaults(t *testing.T) {
	checks := []struct {
		role, right string
		want        bool
	}{
		{RoleOwner, "tariffs.delete", true},
		{RoleAdmin, "workers.update", true}, {RoleAdmin, "tariffs.delete", false},
		{RoleManager, "workers.view", false}, {RoleManager, "warehouse.create", true}, {RoleManager, "chat.create", false},
		{RoleCashier, "cash.create", true}, {RoleCashier, "cash.delete", false}, {RoleCashier, "customers.update", true},
		{RoleOperator, "cash.view", false}, {RoleOperator, "chat.create", true},
	}
	for _, c := range checks {
		if got := Allows(&Membership{Role: c.role}, c.right); got != c.want {
			t.Errorf("GS-249: %s %s = %v, want %v", c.role, c.right, got, c.want)
		}
	}
}

// GS-250 · права кассира после выбора владельца {cash.view: false, warehouse.view: true, warehouse.create: true}:
// склад 200, касса 403, создание категории (warehouse.create) — да, движение склада (warehouse.update) — 403.
func TestGS250_EditCashierRights(t *testing.T) {
	m := &Membership{Role: RoleCashier, Given: ParseGiven([]byte(`{"cash.view": false, "warehouse.view": true, "warehouse.create": true}`))}
	want := map[string]bool{
		RightFor("warehouse"):                       true,
		RightFor("cash-register.index"):             false,
		RightFor("warehouse.categories.store"):      true,
		RightFor("warehouse.stock-movements.store"): false,
	}
	for right, w := range want {
		if got := Allows(m, right); got != w {
			t.Errorf("GS-250: %s = %v, want %v", right, got, w)
		}
	}
}

// GS-006 · кассир без cash.delete не возвращает; с правом от владельца — возвращает.
func TestGS006_CashierRefundNeedsRight(t *testing.T) {
	right := RightFor("cash-register.refund")
	if right != "cash.delete" {
		t.Fatalf("refund right = %q", right)
	}
	if Allows(&Membership{Role: RoleCashier}, right) {
		t.Error("GS-006: cashier refunds without right")
	}
	if !Allows(&Membership{Role: RoleCashier, Given: map[string]bool{"cash.delete": true}}, right) {
		t.Error("GS-006: cashier with right cannot refund")
	}
}

// GS-144 · менеджер удаляет товар только с правом от владельца.
func TestGS144_ManagerProductDelete(t *testing.T) {
	right := RightFor("warehouse.products.destroy")
	if Allows(&Membership{Role: RoleManager}, right) {
		t.Error("GS-144: manager deletes without right")
	}
	if !Allows(&Membership{Role: RoleManager, Given: map[string]bool{"warehouse.delete": true}}, right) {
		t.Error("GS-144: manager with right cannot delete")
	}
}

// GS-253 · кассир с customers.create = false, reservations.view = false.
func TestGS253_HiddenWithoutRight(t *testing.T) {
	m := &Membership{Role: RoleCashier, Given: map[string]bool{"customers.create": false, "reservations.view": false}}
	if Allows(m, RightFor("users.store")) || Allows(m, RightFor("reservations.index")) {
		t.Error("GS-253")
	}
	if !Allows(m, RightFor("users.index")) {
		t.Error("GS-253: other rights stay")
	}
}

// GS-120 · клиент не открывает брони.
func TestGS120_ClientReservations(t *testing.T) {
	if Allows(&Membership{Role: RoleClient}, RightFor("reservations.index")) {
		t.Error("GS-120")
	}
}

// GS-254 · каждое право таблицы маршрутов существует; маршрут не повторяется; каждое право используется.
func TestGS254_EveryRouteRightExists(t *testing.T) {
	seen := map[string]string{}
	used := map[string]bool{}
	for _, rr := range RouteRights {
		if !Exists(rr.Right) {
			t.Errorf("GS-254: right %q does not exist", rr.Right)
		}
		used[rr.Right] = true
		for _, r := range rr.Routes {
			if prev, dup := seen[r]; dup {
				t.Errorf("route %s twice (%s, %s)", r, prev, rr.Right)
			}
			seen[r] = rr.Right
		}
	}
	if len(seen) != 93 || len(RouteRights) != 51 {
		t.Errorf("routes = %d, rights = %d, want 93 and 51 (RequireClubArea::ROUTES)", len(seen), len(RouteRights))
	}
	for _, r := range All() {
		// customer_money.update проверяется в UserController::update() напрямую (ClubPermissions::allows), не маршрутом.
		if !used[r] && r != "customer_money.update" {
			t.Errorf("right %s has no route", r)
		}
	}
	if RightFor("unknown.route") != "" {
		t.Error("unknown route must have no right")
	}
}

// BR-142 · права приложения сотрудника.
func TestBR142_StaffApp(t *testing.T) {
	cases := []struct {
		name  string
		m     *Membership
		guest bool
		want  StaffApp
	}{
		{"owner", &Membership{Role: RoleOwner}, true, StaffApp{true, true, true, true, true, true, true, true}},
		{"admin no guest mode", &Membership{Role: RoleAdmin}, false, StaffApp{true, false, true, true, false, true, true, true}},
		{"cashier", &Membership{Role: RoleCashier}, true, StaffApp{true, true, false, true, true, true, false, false}},
		{"operator", &Membership{Role: RoleOperator}, true, StaffApp{false, false, true, true, true, true, false, false}},
		{"cashier no cash.view", &Membership{Role: RoleCashier, Given: map[string]bool{"cash.view": false}}, true,
			StaffApp{false, true, false, true, true, true, false, false}},
		{"nil", nil, true, StaffApp{}},
	}
	for _, c := range cases {
		if got := StaffAppFor(c.m, c.guest); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestRoles(t *testing.T) {
	if !IsStaff(RoleOwner) || IsStaff(RoleClient) || IsStaff("Пользователь") {
		t.Error("IsStaff")
	}
	if !IsShiftRole(RoleCashier) || !IsShiftRole(RoleOperator) || IsShiftRole(RoleAdmin) {
		t.Error("IsShiftRole")
	}
	if RoleLabel("Пользователь") != "Клиент" || RoleLabel(RoleOwner) != "Владелец" {
		t.Error("RoleLabel")
	}
	for _, s := range Sections {
		l, ok := SectionLabels[s.Name]
		if !ok || len(l.Actions) != len(s.Actions) {
			t.Errorf("labels of %s", s.Name)
		}
		for a := range s.Actions {
			if l.Actions[a] == "" {
				t.Errorf("label %s.%s", s.Name, a)
			}
		}
	}
}

func principalFor(role string, given map[string]bool) principal.Principal {
	m := &Membership{Role: role, Given: given}
	return principal.Principal{UserID: 7, ClubID: 3, Role: role, Permissions: For(m)}
}

func serve(h http.Handler, method string, p *principal.Principal) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/x", nil)
	if p != nil {
		r = r.WithContext(principal.WithPrincipal(r.Context(), *p))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

var ok200 = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

func TestMiddleware(t *testing.T) {
	cashier := principalFor(RoleCashier, nil)
	manager := principalFor(RoleManager, nil)
	noClub := principal.Principal{UserID: 1, Superadmin: true}
	viewing := principal.Principal{UserID: 1, ClubID: 3, Role: RoleOwner, Permissions: All(), Superadmin: true, ReadOnly: true}
	client := principalFor(RoleClient, nil)

	cases := []struct {
		name   string
		h      http.Handler
		method string
		p      *principal.Principal
		status int
		msg    string
	}{
		{"anon", RequirePermission("cash.view")(ok200), "GET", nil, 401, MsgUnauthenticated},
		{"cashier cash.view", RequirePermission("cash.view")(ok200), "GET", &cashier, 200, ""},
		{"cashier refund", RequirePermission("cash.delete")(ok200), "POST", &cashier, 403, MsgNoRight},
		{"cashier tariffs", RequirePermission("tariffs.update")(ok200), "POST", &cashier, 403, MsgNoRight},
		{"manager tariffs", RequirePermission("tariffs.update")(ok200), "POST", &manager, 200, ""},
		{"manager tariffs delete", RequirePermission("tariffs.delete")(ok200), "DELETE", &manager, 403, MsgNoRight},
		{"manager customer money", RequirePermission("customer_money.update")(ok200), "PATCH", &manager, 200, ""},
		{"cashier customer money", RequireSectionPermission("customer_money.update")(ok200), "PATCH", &cashier, 403, MsgNoSectionRight},
		{"no club", RequireClub(ok200), "GET", &noClub, 403, MsgNoClubs},
		{"viewing GET", RequirePermission("tariffs.view")(ok200), "GET", &viewing, 200, ""},
		{"viewing HEAD", RequireClub(ok200), "HEAD", &viewing, 200, ""},
		{"viewing POST", RequirePermission("cash.create")(ok200), "POST", &viewing, 403, MsgReadOnly},
		{"viewing DELETE", RequireClub(ok200), "DELETE", &viewing, 403, MsgReadOnly},
		{"client reservations", RequirePermission("reservations.view")(ok200), "GET", &client, 403, MsgNoRight},
		{"client staff", RequireStaff(ok200), "GET", &client, 403, ""},
		{"cashier staff", RequireStaff(ok200), "GET", &cashier, 200, ""},
		{"role owner,admin: manager", RequireRole(RoleAdmin)(ok200), "GET", &manager, 403, ""},
		{"role admin: owner passes", RequireRole(RoleAdmin)(ok200), "GET", &viewing, 200, ""},
		{"superadmin", RequireSuperadmin(ok200), "POST", &noClub, 200, ""},
		{"not superadmin", RequireSuperadmin(ok200), "GET", &manager, 403, ""},
		{"superadmin anon", RequireSuperadmin(ok200), "GET", nil, 401, MsgUnauthenticated},
		{"auth", RequireAuth(ok200), "GET", &noClub, 200, ""},
		{"auth anon", RequireAuth(ok200), "GET", nil, 401, MsgUnauthenticated},
	}
	for _, c := range cases {
		w := serve(c.h, c.method, c.p)
		if w.Code != c.status {
			t.Errorf("%s: status %d, want %d", c.name, w.Code, c.status)
		}
		if c.msg != "" && !strings.Contains(w.Body.String(), c.msg) {
			t.Errorf("%s: body %s, want %q", c.name, w.Body.String(), c.msg)
		}
	}
}

func TestRequirePermissionUnknownPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	RequirePermission("cash.update")
}

func TestPrincipalCan(t *testing.T) {
	p := principalFor(RoleCashier, map[string]bool{"cash.view": false})
	if p.Can("cash.view") || !p.Can("cash.create") {
		t.Error("Can")
	}
	// Суперадмин сам по себе прав в клубе не имеет (ClubPermissions::allows смотрит только на членство).
	if (principal.Principal{Superadmin: true, ClubID: 3}).Can("cash.view") {
		t.Error("superadmin without membership")
	}
	if (principal.Principal{Permissions: []string{"cash.view"}}).Can("cash.view") {
		t.Error("no club → no rights")
	}
}
