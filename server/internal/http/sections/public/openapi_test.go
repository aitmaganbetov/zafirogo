package public

import (
	"bytes"
	"os"
	"testing"
)

// TestOpenAPISpecMatchesSection — /docs/api показывает ту же спецификацию, что у раздела v1 (api/v1.yaml).
func TestOpenAPISpecMatchesSection(t *testing.T) {
	want, err := os.ReadFile("../../../../../api/v1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(openAPIv1, want) {
		t.Fatal("server/internal/http/sections/public/openapi-v1.yaml отличается от api/v1.yaml: cp api/v1.yaml server/internal/http/sections/public/openapi-v1.yaml")
	}
}
