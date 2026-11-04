package warehouse

import (
	"reflect"
	"testing"

	warehouseapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/warehouse"
)

// Каждая операция раздела либо публичная (проверка в обработчике), либо закрыта правом маршрута Laravel.
func TestEveryOperationHasRight(t *testing.T) {
	if bad := access.Uncovered(reflect.TypeFor[warehouseapi.StrictServerInterface]()); len(bad) > 0 {
		t.Fatal(bad)
	}
}
