package v1_test

import (
	"slices"
	"strings"
	"testing"

	v1 "github.com/aitmaganbetov/zafirogo/server/internal/http/sections/v1"
)

const (
	specPath   = "../../../../../api/v1.yaml"
	legacyPath = "testdata/openapi-v1.legacy.yaml"
)

// TestV1Routes_AllDeclareScope — GS-298: у каждого маршрута v1 есть область (api.scope:*), 17 маршрутов.
func TestV1Routes_AllDeclareScope(t *testing.T) {
	routes := v1.Routes()
	if len(routes) != 17 {
		t.Fatalf("маршрутов %d, в routes/api_v1.php — 17", len(routes))
	}
	for _, r := range routes {
		if r.Scope == "" || !strings.HasPrefix(r.Name, "api.v1.") {
			t.Errorf("%s %s: нет области или имени", r.Method, r.Path)
		}
	}
}

// TestV1Routes_DescribedInSpec — каждый маршрут описан в api/v1.yaml и в старой openapi-v1.yaml, и наоборот.
func TestV1Routes_DescribedInSpec(t *testing.T) {
	var ours []string
	for _, r := range v1.Routes() {
		ours = append(ours, r.Method+" "+r.Path)
	}
	slices.Sort(ours)
	for _, s := range []*openAPI{loadSpec(t, "api/v1.yaml", specPath), loadSpec(t, "legacy", legacyPath)} {
		if got := s.operations(); !slices.Equal(got, ours) {
			t.Errorf("%s: операции спецификации\n%v\nмаршруты\n%v", s.name, got, ours)
		}
	}
}

func TestMaskPhone(t *testing.T) {
	for in, want := range map[string]string{"+77001234567": "+7 *** *** 45 67", "8 (700) 123-45-67": "+8 *** *** 45 67"} {
		if got := v1.MaskPhone(in); got == nil || *got != want {
			t.Errorf("%s: %v", in, got)
		}
	}
	if v1.MaskPhone("12345") != nil {
		t.Error("short phone must be null")
	}
}

// TestCursorLaravelFormat — курсор как Cursor::encode() Laravel (снят PHP: base64 JSON без «=»).
func TestCursorLaravelFormat(t *testing.T) {
	const php = "eyJwcm9kdWN0cy5pZCI6NSwiX3BvaW50c1RvTmV4dEl0ZW1zIjp0cnVlfQ"
	if got := v1.EncodeCursor("products.id", 5); got != php {
		t.Fatalf("cursor %s, PHP %s", got, php)
	}
	if v1.DecodeCursor(php, "products.id") != 5 || v1.DecodeCursor("garbage", "products.id") != 0 ||
		v1.DecodeCursor(php, "tariffs.id") != 0 {
		t.Fatal("decode")
	}
}
