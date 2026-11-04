package public

import (
	"reflect"
	"testing"

	publicapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/public"
)

// Каждая операция раздела либо публичная (проверка в обработчике), либо закрыта правом маршрута Laravel.
func TestEveryOperationHasRight(t *testing.T) {
	if bad := access.Uncovered(reflect.TypeFor[publicapi.StrictServerInterface]()); len(bad) > 0 {
		t.Fatal(bad)
	}
}
