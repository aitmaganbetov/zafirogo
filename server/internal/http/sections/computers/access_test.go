package computers

import (
	"reflect"
	"testing"

	computersapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/computers"
)

func TestEveryOperationHasRight(t *testing.T) {
	if bad := access.Uncovered(reflect.TypeFor[computersapi.StrictServerInterface]()); len(bad) > 0 {
		t.Fatal(bad)
	}
}

func TestParseRelease(t *testing.T) {
	v, u := parseRelease([]byte(`{"latest_version":"2.5.0","download_url":"https://example.org/a.exe"}`))
	if v != "2.5.0" || u == nil || *u != "https://example.org/a.exe" {
		t.Fatal(v, u)
	}
	if v, u := parseRelease([]byte(`{"latest_version":"2.5","download_url":"javascript:alert(1)"}`)); v != "" || u != nil {
		t.Fatal(v, u)
	}
	if _, u := parseRelease([]byte(`{"latest_version":"2.5.0","download_url":"http://x"}`)); u != nil {
		t.Fatal(u)
	}
}
