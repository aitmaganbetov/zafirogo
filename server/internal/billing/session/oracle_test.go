package session

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// Фикстура для PHP-оракула: входы SessionSettlement::settle() и ответ Go. Оракул (tools/php-oracle)
// заполняет поле "php" фактическим ответом старого кода; тест сверяет Go с "go" (дрейф) и с "php"
// (расхождение со старым кодом). Регенерация входов и ответа Go (поле "php" сохраняется):
//
//	go test ./internal/billing/session/ -run TestSettleOracleFixture -update-settle-fixture
var updateSettleFixture = flag.Bool("update-settle-fixture", false, "перезаписать testdata/settle_oracle.json")

const settleFixturePath = "testdata/settle_oracle.json"

// timeLayout — с микросекундами: часть сценариев проверяет усечение долей секунды now.
const timeLayout = "2006-01-02T15:04:05.000000Z07:00"

type fixturePart struct {
	ID         int64   `json:"id"`
	UserID     int64   `json:"user_id"`
	Kind       Kind    `json:"kind"`
	TariffID   *int64  `json:"tariff_id"`
	Name       string  `json:"name"`
	Minutes    int64   `json:"minutes"`
	MainCents  int64   `json:"main_cents"`
	BonusCents int64   `json:"bonus_cents"`
	ExpiresAt  *string `json:"expires_at"`
}

type fixturePackage struct {
	TariffID    *int64 `json:"tariff_id"`
	MinutesLeft int64  `json:"minutes_left"`
	ExpiresAt   string `json:"expires_at"`
}

type fixtureResult struct {
	RefundMain  int64            `json:"refund_main"`
	RefundBonus int64            `json:"refund_bonus"`
	KeptMinutes int64            `json:"kept_minutes"`
	Packages    []fixturePackage `json:"packages"` // в порядке создания (части от последней к первой)
	// TotalSpentDecrease — на сколько тиынов уменьшились club_memberships.total_spent (при достаточных тратах).
	TotalSpentDecrease int64 `json:"total_spent_decrease"`
}

type fixtureScenario struct {
	ID         string         `json:"id"`
	CustomerID int64          `json:"customer_id"`
	Now        string         `json:"now"`
	EndsAt     *string        `json:"ends_at"`
	Parts      []fixturePart  `json:"parts"`
	Go         fixtureResult  `json:"go"`
	PHP        *fixtureResult `json:"php"`
}

type fixtureFile struct {
	About     string            `json:"about"`
	Scenarios []fixtureScenario `json:"scenarios"`
}

const fixtureAbout = "Входы SessionSettlement::settle() (app/Services/SessionSettlement.php) и ответ Go-пакета " +
	"server/internal/billing/session. Для оракула: Carbon::setTestNow(now); вставить parts в session_purchases " +
	"(session_id общий, id как есть, created_at любой); клиенту — member_balances и club_memberships.total_spent " +
	"с запасом; user_id частей, отличный от customer_id, — другой клиент клуба; вызвать settle(computer, session_id, customer_id, ends_at ? Carbon::parse(ends_at) : null, customer_id); " +
	"записать в поле php результат (refund_main, refund_bonus, kept_minutes), созданные time_packages " +
	"(tariff_id, minutes_left, expires_at UTC) по возрастанию id и уменьшение total_spent в тиынах. " +
	"Время — UTC, now с микросекундами."

func fmtTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func fmtTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := fmtTime(*t)
	return &s
}

func parseTimePtr(t *testing.T, s *string) *time.Time {
	t.Helper()
	if s == nil {
		return nil
	}
	v, err := time.Parse(timeLayout, *s)
	if err != nil {
		t.Fatal(err)
	}
	return &v
}

func toResult(s Settlement) fixtureResult {
	r := fixtureResult{RefundMain: int64(s.RefundMain), RefundBonus: int64(s.RefundBonus), KeptMinutes: s.KeptMinutes,
		TotalSpentDecrease: int64(s.LoyaltySpendingDecrease), Packages: []fixturePackage{}}
	for _, p := range s.Packages {
		r.Packages = append(r.Packages, fixturePackage{TariffID: p.TariffID, MinutesLeft: p.MinutesLeft, ExpiresAt: fmtTime(p.ExpiresAt)})
	}
	return r
}

// fixtureCases — сценарии GS/GAP/BR (goldenSettleCases) + детерминированные случайные (граничные дроби тиына,
// чужие части, истёкшие лимиты, микросекунды now).
func fixtureCases() []settleCase {
	cases := goldenSettleCases()
	r := rand.New(rand.NewPCG(20261030, 30))
	for i := range 20 {
		parts, ends := randomSession(r)
		// Секунды и микросекунды now случайны: проверка усечения getTimestamp().
		span := int64(ends.Sub(t0) / time.Second)
		now := t0.Add(time.Duration(r.Int64N(span+1))*time.Second + time.Duration(r.Int64N(1_000_000))*time.Microsecond)
		for j := range parts {
			parts[j].TariffID = nil
			if parts[j].Kind == KindTariff {
				parts[j].TariffID = i64(100 + parts[j].ID)
			}
		}
		cases = append(cases, settleCase{name: fmt.Sprintf("RND-%02d", i+1), parts: parts, endsAt: &ends, now: now})
	}
	return cases
}

func TestSettleOracleFixture(t *testing.T) {
	var old fixtureFile
	if b, err := os.ReadFile(settleFixturePath); err == nil {
		if err := json.Unmarshal(b, &old); err != nil {
			t.Fatal(err)
		}
	} else if !*updateSettleFixture {
		t.Fatal(err)
	}
	php := map[string]*fixtureResult{}
	for _, s := range old.Scenarios {
		php[s.ID] = s.PHP
	}

	if *updateSettleFixture {
		out := fixtureFile{About: fixtureAbout}
		for _, c := range fixtureCases() {
			sc := fixtureScenario{ID: c.name, CustomerID: customer, Now: fmtTime(c.now), EndsAt: fmtTimePtr(c.endsAt), PHP: php[c.name]}
			for _, p := range c.parts {
				sc.Parts = append(sc.Parts, fixturePart{ID: p.ID, UserID: p.UserID, Kind: p.Kind, TariffID: p.TariffID, Name: p.Name,
					Minutes: p.Minutes, MainCents: int64(p.MainCents), BonusCents: int64(p.BonusCents), ExpiresAt: fmtTimePtr(p.ExpiresAt)})
			}
			got, err := Settle(c.parts, customer, c.endsAt, c.now)
			if err != nil {
				t.Fatal(err)
			}
			sc.Go = toResult(got)
			out.Scenarios = append(out.Scenarios, sc)
		}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(settleFixturePath), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(settleFixturePath, append(b, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		old = out
	}

	if len(old.Scenarios) < 30 {
		t.Fatalf("в фикстуре %d сценариев, нужно ≥ 30", len(old.Scenarios))
	}
	checkedPHP := 0
	for _, sc := range old.Scenarios {
		t.Run(sc.ID, func(t *testing.T) {
			parts := make([]Purchase, 0, len(sc.Parts))
			for _, p := range sc.Parts {
				parts = append(parts, Purchase{ID: p.ID, UserID: p.UserID, Kind: p.Kind, TariffID: p.TariffID, Name: p.Name, Minutes: p.Minutes,
					MainCents: money.Tiyn(p.MainCents), BonusCents: money.Tiyn(p.BonusCents), ExpiresAt: parseTimePtr(t, p.ExpiresAt)})
			}
			got, err := Settle(parts, sc.CustomerID, parseTimePtr(t, sc.EndsAt), *parseTimePtr(t, &sc.Now))
			if err != nil {
				t.Fatal(err)
			}
			res := toResult(got)
			if !equalResult(res, sc.Go) {
				t.Errorf("Go изменился: %+v, в фикстуре %+v (перегенерируйте осознанно)", res, sc.Go)
			}
			if sc.PHP != nil {
				checkedPHP++
				if !equalResult(res, *sc.PHP) {
					t.Errorf("расхождение с PHP: Go %+v, PHP %+v", res, *sc.PHP)
				}
			}
		})
	}
	if checkedPHP == 0 {
		t.Log("ответов PHP-оракула в фикстуре ещё нет (docs/open-questions/stage1-session.md, S1-SES-01)")
	}
}

func equalResult(a, b fixtureResult) bool {
	if a.RefundMain != b.RefundMain || a.RefundBonus != b.RefundBonus || a.KeptMinutes != b.KeptMinutes ||
		a.TotalSpentDecrease != b.TotalSpentDecrease || len(a.Packages) != len(b.Packages) {
		return false
	}
	for i := range a.Packages {
		x, y := a.Packages[i], b.Packages[i]
		if x.MinutesLeft != y.MinutesLeft || !sameInstant(x.ExpiresAt, y.ExpiresAt) || (x.TariffID == nil) != (y.TariffID == nil) ||
			(x.TariffID != nil && *x.TariffID != *y.TariffID) {
			return false
		}
	}
	return true
}

// sameInstant сравнивает моменты времени, записанные в любом из форматов RFC 3339 (PHP пишет «+00:00»).
func sameInstant(a, b string) bool {
	x, err1 := time.Parse(time.RFC3339Nano, a)
	y, err2 := time.Parse(time.RFC3339Nano, b)
	return err1 == nil && err2 == nil && x.Equal(y)
}
