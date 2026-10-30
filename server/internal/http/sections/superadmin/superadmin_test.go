package superadmin

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	superadminapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/superadmin"
)

// Все операции кабинета — за RequireSuperadmin (в Access — как «публичные» для прав клуба).
func TestEveryOperationIsCovered(t *testing.T) {
	if bad := access.Uncovered(reflect.TypeFor[superadminapi.StrictServerInterface]()); len(bad) > 0 {
		t.Fatal(bad)
	}
}

func TestPHPNum(t *testing.T) {
	for _, c := range []struct {
		v      float64
		places int
		want   string
	}{
		{1.005, 2, "1.01"}, {2, 1, "2"}, {0.25, 1, "0.3"}, {1440.0 / 1440, 1, "1"}, {10.0 / 15, 1, "0.7"}, {-0.0001, 1, "0"},
	} {
		if got := phpNum(c.v, c.places); got != c.want {
			t.Errorf("phpNum(%v, %d) = %q, want %q", c.v, c.places, got, c.want)
		}
	}
	if got := numberFormat(1234567, " "); got != "1 234 567" {
		t.Errorf("numberFormat = %q", got)
	}
}

// LineChart::make(): точки, разрывы без значения, шкала.
func TestMakeChart(t *testing.T) {
	c := makeChart([]int64{0, 10, 20}, []series{{label: "a", points: map[int64]float64{0: 1, 20: 2}}}, nil)
	if len(c.Lines) != 1 || c.Lines[0].Color != "#7c3aed" {
		t.Fatalf("lines: %+v", c.Lines)
	}
	if got := strings.Join(c.Lines[0].Segments, "|"); got != "0,87.3|600,14.5" {
		t.Errorf("segments = %q", got)
	}
	if c.MaxLabel != "2.2" || *c.From != 0 || *c.To != 20 {
		t.Errorf("max %q from %v to %v", c.MaxLabel, *c.From, *c.To)
	}
	hundred := 100.0
	if c := makeChart([]int64{5}, nil, &hundred); c.MaxLabel != "100" || len(c.Lines) != 0 {
		t.Errorf("empty chart: %+v", c)
	}
}

// AgentUpdate::isOutdated(): как version_compare, нечитаемая версия — старая.
func TestIsOutdated(t *testing.T) {
	for _, c := range []struct {
		v, latest string
		want      bool
	}{
		{"1.2.0", "1.10.0", true}, {"1.10.0", "1.2.0", false}, {"1.2.0", "1.2.0", false}, {"1.2.0-beta", "1.2.1", true},
		{"dev-build", "1.0.0", true}, {"1.0.0", "", false},
	} {
		if got := isOutdated(c.v, c.latest); got != c.want {
			t.Errorf("isOutdated(%q, %q) = %v", c.v, c.latest, got)
		}
	}
	if !clubAllowsAutoUpdate("") || !clubAllowsAutoUpdate("true") || clubAllowsAutoUpdate("false") || clubAllowsAutoUpdate("0") {
		t.Error("clubAllowsAutoUpdate")
	}
}

func TestBadge(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	s := func(v string) *string { return &v }
	for _, c := range []struct {
		status        *string
		trial, period *time.Time
		label         string
		active        bool
	}{
		{nil, nil, nil, "Нет подписки", false},
		{s("trialing"), &future, nil, "Пробный период", true},
		{s("trialing"), &past, nil, "Неактивна", false},
		{s("active"), nil, &future, "Активна", true},
		{s("active"), nil, nil, "Активна", true},
		{s("active"), nil, &past, "Истекла", false},
		{s("expired"), nil, &past, "Истекла", false},
		{s("suspended"), nil, &future, "Приостановлена", false},
	} {
		b := badge(c.status, c.trial, c.period, now)
		if b.Label != c.label || b.Active != c.active {
			t.Errorf("%v: %+v", c.status, b)
		}
	}
}

// fputcsv(';', '"', ”), защита формул и разбор с автоопределением разделителя.
func TestCSV(t *testing.T) {
	var b bytes.Buffer
	putCSV(&b, []string{"id", "a b", `say "hi"`, protectCell("=1+1"), "x;y"})
	if got := b.String(); got != "id;\"a b\";\"say \"\"hi\"\"\";'=1+1;\"x;y\"\n" {
		t.Errorf("csv = %q", got)
	}
	if unprotectCell("'=1+1") != "=1+1" || unprotectCell("'abc") != "'abc" {
		t.Error("unprotect")
	}
	rows, err := parseCSV([]byte("\xEF\xBB\xBFid,group,ru,en,kk\nh1,*,\"Привет, :name\",Hi,Сәлем :name\n"))
	if err != nil || len(rows) != 2 || rows[1][2] != "Привет, :name" {
		t.Fatalf("parse: %v %q", err, rows)
	}
	if _, err := parseCSV([]byte{0xff, 0xfe}); err == nil {
		t.Error("non-UTF-8 accepted")
	}
	if !placeholdersMatch("Привет, :name и :Count", ":count :NAME") || placeholdersMatch(":name", "без подстановки") {
		t.Error("placeholdersMatch")
	}
}
