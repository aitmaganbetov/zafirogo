package layout

import (
	"reflect"
	"testing"

	layoutapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/layout"
)

// Каждая операция раздела либо публичная (проверка в обработчике), либо закрыта правом маршрута Laravel.
func TestEveryOperationHasRight(t *testing.T) {
	if bad := access.Uncovered(reflect.TypeFor[layoutapi.StrictServerInterface]()); len(bad) > 0 {
		t.Fatal(bad)
	}
}
