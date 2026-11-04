package main

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

func TestTiyn(t *testing.T) {
	ok := map[string]money.Tiyn{"1500.00": 150000, "15750.5": 1575050, "80": 8000, "0.01": 1, "-3.10": -310, "": 0}
	for in, want := range ok {
		s := in
		got, err := tiyn(&s)
		if err != nil || got != want {
			t.Errorf("tiyn(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"1.005", "abc", ".5", "1e3"} {
		s := in
		if _, err := tiyn(&s); err == nil {
			t.Errorf("tiyn(%q): want error", in)
		}
	}
}

// Фикстура читается целиком, все суммы и даты разбираются (опечатка в demo.json ловится без базы).
func TestDemoFixture(t *testing.T) {
	raw, err := os.ReadFile("../../../tools/visual/fixtures/demo.json")
	if err != nil {
		t.Fatal(err)
	}
	var f Fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if _, err := parseAt(f.Now); err != nil {
		t.Fatal(err)
	}
	for _, m := range f.Memberships {
		for _, v := range []*string{m.Balance, m.BonusBalance, m.TotalSpent} {
			if _, err := tiyn(v); err != nil {
				t.Error(err)
			}
		}
	}
	for _, op := range f.Operations {
		if _, err := parseAt(op.At); err != nil {
			t.Error(err)
		}
	}
	for _, p := range f.Products {
		if _, err := tiyn(&p.Price); err != nil {
			t.Error(err)
		}
	}
	if f.Login.PasswordBcrypt == "" || len(f.Login.Roles) == 0 {
		t.Error("login: нужны password_bcrypt и roles")
	}
}
