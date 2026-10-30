package customers

import (
	"reflect"
	"testing"

	customersapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/customers"
)

// Каждая операция раздела либо публичная (проверка в обработчике), либо закрыта правом маршрута Laravel.
func TestEveryOperationHasRight(t *testing.T) {
	if bad := access.Uncovered(reflect.TypeFor[customersapi.StrictServerInterface]()); len(bad) > 0 {
		t.Fatal(bad)
	}
}
