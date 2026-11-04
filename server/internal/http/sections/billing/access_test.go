package billing

import (
	"reflect"
	"testing"

	billingapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/billing"
)

// Каждая операция раздела либо публичная (проверка в обработчике), либо закрыта правом маршрута Laravel.
func TestEveryOperationHasRight(t *testing.T) {
	if bad := access.Uncovered(reflect.TypeFor[billingapi.StrictServerInterface]()); len(bad) > 0 {
		t.Fatal(bad)
	}
}
