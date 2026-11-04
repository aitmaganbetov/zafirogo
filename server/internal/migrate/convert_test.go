package migrate

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

func TestTiynFromDecimal(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want money.Tiyn
		err  bool
	}{
		{"0.00", 0, false},
		{"1500.50", 150050, false},
		{"15750.50", 1575050, false},
		{"-150.50", -15050, false}, // Q-004: отрицательный остаток — точно, без ошибки знака старого formatCents
		{"0.01", 1, false},
		{"-0.01", -1, false},
		{"99999999999.99", 9999999999999, false},
		{"1.005", 0, true}, // decimal(…,2) таких не даёт; три знака — не округляем молча
		{"1e3", 0, true},
		{"", 0, true},
	} {
		got, err := tiynFromDecimal(tc.in)
		if (err != nil) != tc.err || (!tc.err && got != tc.want) {
			t.Errorf("tiynFromDecimal(%q) = %d, %v; want %d, err=%v", tc.in, got, err, tc.want, tc.err)
		}
	}
}

func TestParseMySQLTime(t *testing.T) {
	almaty, err := time.LoadLocation("Asia/Almaty")
	if err != nil {
		t.Skip("нет tzdata:", err)
	}
	got, err := parseMySQLTime("2026-10-15 04:00:00", time.UTC)
	if err != nil || !got.Equal(time.Date(2026, 10, 15, 4, 0, 0, 0, time.UTC)) {
		t.Fatalf("UTC: %s, %v", got, err)
	}
	// Источник в поясе клуба (если app.timezone был не UTC): та же строка — на 5 часов раньше в UTC.
	got, err = parseMySQLTime("2026-10-15 09:00:00.250000", almaty)
	if err != nil || !got.Equal(time.Date(2026, 10, 15, 4, 0, 0, 250_000_000, time.UTC)) {
		t.Fatalf("Asia/Almaty: %s, %v", got, err)
	}
	if _, err := parseMySQLTime("0000-00-00 00:00:00", time.UTC); !errors.Is(err, errBadTime) {
		t.Fatalf("нулевая дата: %v", err)
	}
}

func TestColumnConvert(t *testing.T) {
	s := func(v string) *string { return &v }
	cases := []struct {
		kind kind
		in   *string
		want *string
		err  error
	}{
		{kindBool, s("0"), s("f"), nil},
		{kindBool, s("1"), s("t"), nil},
		{kindBool, nil, nil, nil},
		{kindTiyn, s("25000.00"), s("2500000"), nil},
		{kindTime, s("2026-10-13 04:00:00"), s("2026-10-13T04:00:00Z"), nil},
		{kindJSON, s(`{"items":[{"price":1500.5}]}`), s(`{"items":[{"price":1500.5}]}`), nil},
		{kindJSON, s(`{"a":`), nil, errBadJSON},
		{kindJSON, s(`{"a":"\u0000"}`), nil, errNulJSON},
		{kindText, s("Пользователь"), s("Пользователь"), nil},
		{kindNumeric, s("10.00"), s("10.00"), nil},
	}
	for _, tc := range cases {
		c := column{Name: "x", Kind: tc.kind}
		got, err := c.convert(tc.in, time.UTC)
		if !errors.Is(err, tc.err) {
			t.Errorf("%s %v: err %v, want %v", tc.kind, deref(tc.in), err, tc.err)
			continue
		}
		if deref(got) != deref(tc.want) {
			t.Errorf("%s %v: %v, want %v", tc.kind, deref(tc.in), deref(got), deref(tc.want))
		}
	}
}

func TestPlanColumns(t *testing.T) {
	tbl := table{Name: "member_balances", Omit: map[string]string{"balance": "ledger"}}
	my := []mysqlColumn{
		{Name: "id", DataType: "bigint", Type: "bigint unsigned"},
		{Name: "balance", DataType: "decimal", Type: "decimal(12,2)"},
		{Name: "total", DataType: "decimal", Type: "decimal(12,2)"},
		{Name: "discount", DataType: "decimal", Type: "decimal(5,2)"},
		{Name: "flag", DataType: "tinyint", Type: "tinyint(1)"},
		{Name: "at", DataType: "timestamp", Type: "timestamp"},
		{Name: "data", DataType: "json", Type: "json"},
		{Name: "telegram_id", DataType: "varchar", Type: "varchar(255)"},
	}
	pg := []pgColumn{
		{Name: "id", Type: "bigint"}, {Name: "balance", Type: "bigint"}, {Name: "total", Type: "bigint"},
		{Name: "discount", Type: "numeric"}, {Name: "flag", Type: "boolean"}, {Name: "at", Type: "timestamp with time zone"},
		{Name: "data", Type: "jsonb"}, {Name: "zone_id", Type: "bigint"},
	}
	cols, unmapped, err := planColumns(tbl, my, pg)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]kind{}
	for _, c := range cols {
		kinds[c.Name] = c.Kind
	}
	want := map[string]kind{"id": kindText, "total": kindTiyn, "discount": kindNumeric, "flag": kindBool, "at": kindTime, "data": kindJSON}
	for name, k := range want {
		if kinds[name] != k {
			t.Errorf("%s: %s, want %s", name, kinds[name], k)
		}
	}
	if _, ok := kinds["balance"]; ok {
		t.Error("balance (Omit) не должен переноситься")
	}
	if len(unmapped) != 1 || unmapped[0] != "telegram_id" {
		t.Errorf("unmapped = %v", unmapped)
	}
	// Денежная колонка не с двумя знаками — ошибка схемы, а не молчаливое округление.
	_, _, err = planColumns(table{Name: "t"}, []mysqlColumn{{Name: "p", DataType: "decimal", Type: "decimal(12,4)"}},
		[]pgColumn{{Name: "p", Type: "bigint"}})
	if err == nil {
		t.Error("decimal(12,4) → bigint должен быть ошибкой")
	}
}

func TestParseCheckIn(t *testing.T) {
	def := "CHECK (((status)::text = ANY ((ARRAY['open'::character varying, 'closed'::character varying, 'it''s'::character varying])::text[])))"
	col, values, ok := parseCheckIn(def)
	if !ok || col != "status" || len(values) != 3 || !values["open"] || !values["closed"] || !values["it's"] {
		t.Fatalf("parseCheckIn = %q %v %v", col, values, ok)
	}
	if _, _, ok := parseCheckIn("CHECK ((quantity >= 0))"); ok {
		t.Fatal("CHECK без списка значений не enum")
	}
}

func TestPGWhere(t *testing.T) {
	where, args := pgWhere("id = ANY({ids})", 7, []int64{1, 2}, "cut")
	if where != "id = ANY($1)" || len(args) != 1 {
		t.Fatalf("%q %v", where, args)
	}
	where, args = pgWhere("club_id = {club} AND created_at >= {cutoff}", 7, nil, "cut")
	if where != "club_id = $1 AND created_at >= $2" || len(args) != 2 || args[0] != int64(7) || args[1] != "cut" {
		t.Fatalf("%q %v", where, args)
	}
}

func TestInsertSQL(t *testing.T) {
	got := insertSQL("translations", []column{{Name: "group", Cast: "character varying"}, {Name: "id", Cast: "bigint"}}, nil)
	want := `INSERT INTO "translations" ("group", "id") SELECT u.c1::character varying, u.c2::bigint FROM unnest($1::text[], $2::text[]) AS u(c1, c2)`
	if got != want {
		t.Fatalf("\n%s\n%s", got, want)
	}
}

func TestUsersWhere(t *testing.T) {
	o := &Options{}
	o.normalize()
	where, args := usersWhere(coreTables(), o)
	if !strings.HasPrefix(where, "id IN (SELECT user_id FROM club_memberships WHERE (club_id = ?)") {
		t.Fatalf("where = %s", where)
	}
	if strings.Count(where, "?") != len(args) {
		t.Fatalf("плейсхолдеров %d, аргументов %d", strings.Count(where, "?"), len(args))
	}
	bound := bindClub(args, 42)
	for i, a := range bound {
		if _, ok := args[i].(clubMarker); ok && a != int64(42) {
			t.Fatalf("аргумент %d = %v", i, a)
		}
	}
}

func TestMonthsBetween(t *testing.T) {
	from := time.Date(2023, 5, 1, 0, 0, 0, 0, time.UTC)
	if got := monthsBetween(from, time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)); got != 44 {
		t.Fatalf("monthsBetween = %d", got)
	}
	if got := monthsBetween(from, from); got != 1 {
		t.Fatalf("monthsBetween = %d", got)
	}
}

func TestCheckSettings(t *testing.T) {
	r := &run{o: &Options{}, rep: newReport("plan"), club: club{ID: 1, Slug: "a"}}
	r.checkSettingsJSON([]byte(`{"club":{"session_idle_timeout":"30 сек","booking_interval":30,"agent_shop_enabled":true},
		"finances":{"loyalty_rules":[],"tax_rate":12,"auto_billing":"1",
			"loyalty_levels":[{"spent":20000,"percent":5},{"spent":"20000","percent":7}],
			"topup_bonus_levels":{"a":{"amount":1000,"percent":5}}},"extra":{}}`))
	var values []string
	for _, f := range r.rep.Find(CodeSettingsFormat) {
		values = append(values, f.Value)
	}
	joined := strings.Join(values, "|")
	for _, want := range []string{`club.session_idle_timeout="30 сек"`, "finances.loyalty_rules", "finances.tax_rate",
		`finances.auto_billing="1"`, "extra", "finances.loyalty_levels.spent=20000", "finances.topup_bonus_levels"} {
		if !strings.Contains(joined, want) {
			t.Errorf("нет находки %q в %s", want, joined)
		}
	}
	if strings.Contains(joined, "booking_interval") || strings.Contains(joined, "agent_shop_enabled") {
		t.Errorf("лишняя находка: %s", joined)
	}

	// Q-022: клуб legacy без settings — файл подставляется, без файла — дефолты (предупреждение).
	r = &run{o: &Options{LegacySettingsFile: []byte(`{"club":{"booking_interval":15}}`)}, rep: newReport("plan"),
		club: club{ID: 1, Slug: legacySlug}}
	row := []*string{nil}
	idx := map[string]int{"settings": 0}
	set := func(name, v string) { row[idx[name]] = &v }
	if err := r.checkClubSettings(row, idx, set); err != nil {
		t.Fatal(err)
	}
	if row[0] == nil || *row[0] != `{"club":{"booking_interval":15}}` || !r.rep.Has(Warning, CodeSettingsLegacy) {
		t.Fatalf("legacy settings: %v", deref(row[0]))
	}
}

func TestReportAggregates(t *testing.T) {
	r := newReport("plan")
	for i := range 7 {
		r.add(Warning, CodeOldRole, "club_memberships", "role", "Пользователь", string(rune('1'+i)), "старая роль")
	}
	r.add(Blocker, CodeEnumOutsideCheck, "club_computers", "status", "busy", "100", "вне CHECK")
	if len(r.Findings) != 2 || r.Findings[0].Count != 7 || len(r.Findings[0].Samples) != maxSamples {
		t.Fatalf("findings: %+v", r.Findings[0])
	}
	if r.Count(Blocker) != 1 || r.Count(Warning) != 7 {
		t.Fatalf("counts: %d %d", r.Count(Blocker), r.Count(Warning))
	}
	var b strings.Builder
	if err := r.WriteText(&b); err != nil || !strings.Contains(b.String(), "БЛОКЕРЫ") {
		t.Fatalf("text: %v\n%s", err, b.String())
	}
}
