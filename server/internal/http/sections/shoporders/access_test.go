package shoporders

import (
	"reflect"
	"testing"

	shopordersapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/shoporders"
)

// Каждая операция раздела либо публичная (проверка в обработчике), либо закрыта правом маршрута Laravel.
func TestEveryOperationHasRight(t *testing.T) {
	if bad := access.Uncovered(reflect.TypeFor[shopordersapi.StrictServerInterface]()); len(bad) > 0 {
		t.Fatal(bad)
	}
}
