//go:build integration

package httpserver

// Поиск клиентов на странице «Пользователи» (старый Zafiro 35c36a0): имя, email, телефон в любом формате.
// Laravel: tests/Feature/UsersTest.php «the user list finds customers by name, phone in any format or email».

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
)

func TestAdminCustomersSearch(t *testing.T) {
	e := newAdminEnv(t)
	_, owner := e.staff("Владелец", "owner")
	seq := adminSeq.Add(1)
	// Телефоны уникальны на всю платформу — цифры от счётчика теста.
	phone := func(n int64) string { return fmt.Sprintf("+7700%07d", seq*10+n) }
	add := func(name, ph, email string) int64 {
		id := e.customer(owner, name, 0, 0)
		var em any
		if email != "" {
			em = email
		}
		e.exec("UPDATE users SET phone = $2, email = $3 WHERE id = $1", id, ph, em)
		return id
	}
	// В email — метка теста буквами: цифры в запросе ищутся ещё и по телефону (от трёх цифр), а телефоны
	// клиентов теста содержат номер теста.
	tag := strings.Map(func(r rune) rune { return 'a' + r - '0' }, fmt.Sprint(seq))
	ivan := phone(1)
	add("Иван Петров", ivan, "ivan."+tag+"@example.com")
	dana := phone(2)
	add("Дана Ахметова", dana, "dana."+tag+"@example.com")
	add("Ерлан", phone(3), "")
	// Сотрудник с подходящим именем — не клиент.
	e.staff("Иван Кассир", "cashier")
	// Клиент другого клуба с тем же именем — не виден.
	other := newAdminEnv(t)
	_, otherOwner := other.staff("Другой владелец", "owner")
	other.customer(otherOwner, "Иван Петров", 0, 0)

	found := func(q string, extra ...string) ([]string, resp) {
		t.Helper()
		path := "/api/customers?q=" + url.QueryEscape(q)
		for _, x := range extra {
			path += "&" + x
		}
		r := e.get(owner, path)
		expect(t, r, 200, "search "+q)
		var names []string
		for _, it := range r.body["items"].([]any) {
			names = append(names, obj(it)["name"].(string))
		}
		return names, r
	}
	// «8 700 123-45-67»: 11 цифр с ведущей 8 → 7.
	local := "8 " + ivan[2:5] + " " + ivan[5:8] + "-" + ivan[8:10] + "-" + ivan[10:]
	for _, c := range []struct {
		q    string
		want []string
	}{
		{"Петров", []string{"Иван Петров"}},
		{"иван", []string{"Иван Петров"}},
		{local, []string{"Иван Петров"}},
		{dana[7:10] + " " + dana[10:], []string{"Дана Ахметова"}},
		{"DANA." + strings.ToUpper(tag) + "@", []string{"Дана Ахметова"}},
		{"  Ерлан  ", []string{"Ерлан"}},
		{"", []string{"Дана Ахметова", "Ерлан", "Иван Петров"}},
		// Меньше трёх цифр — телефон не ищется.
		{"+7", nil},
		// % и _ — буквально (в Laravel не экранировались: docs/open-questions/upstream2-search.md).
		{"%", nil},
		{"_", nil},
		{"нет такого", nil},
	} {
		got, r := found(c.q)
		slices.Sort(got)
		if !slices.Equal(got, c.want) || num(r.body["total"]) != int64(len(c.want)) {
			t.Fatalf("q=%q: %v total %v, want %v", c.q, got, r.body["total"], c.want)
		}
	}

	// Keyset с поиском: страницы вперёд и назад остаются в пределах найденного.
	q := "." + tag + "@example"
	p1, r1 := found(q, "limit=1")
	next := num(r1.body["next_after_id"])
	if len(p1) != 1 || next == 0 || num(r1.body["total"]) != 2 {
		t.Fatalf("page 1 %v", r1.body)
	}
	p2, r2 := found(q, "limit=1", fmt.Sprintf("after_id=%d", next))
	if len(p2) != 1 || p2[0] == p1[0] || r2.body["next_after_id"] != nil || r2.body["prev_before_id"] == nil {
		t.Fatalf("page 2 %v", r2.body)
	}
	back, _ := found(q, "limit=1", fmt.Sprintf("before_id=%d", num(r2.body["prev_before_id"])))
	if !slices.Equal(back, p1) {
		t.Fatalf("back %v, want %v", back, p1)
	}
	expect(t, e.get(owner, "/api/customers?q="+url.QueryEscape(strings.Repeat("я", 101))), 422, "q longer than 100")
	expect(t, e.get(owner, "/api/customers?q="+url.QueryEscape(" "+strings.Repeat("я", 100)+" ")), 200, "100 after trim")
}
