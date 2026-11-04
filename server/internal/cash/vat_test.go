package cash

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// testdata/vat.json — общие векторы с фронтом (web/src/features/cash/vat.test.ts): total_tiyn → vat_tiyn.
func TestVATIncluded(t *testing.T) {
	raw, err := os.ReadFile("testdata/vat.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Total int64 `json:"total_tiyn"`
		VAT   int64 `json:"vat_tiyn"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 5 {
		t.Fatal("too few vectors")
	}
	for _, c := range cases {
		if got := VATIncluded(money.Tiyn(c.Total)); int64(got) != c.VAT {
			t.Errorf("VATIncluded(%d) = %d, want %d", c.Total, got, c.VAT)
		}
	}
}
