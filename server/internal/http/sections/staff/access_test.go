package staff

import (
	"reflect"
	"testing"

	staffapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/staff"
)

// Каждая операция раздела либо публичная (проверка роли в обработчике), либо закрыта правом маршрута Laravel.
func TestEveryOperationHasRight(t *testing.T) {
	if bad := access.Uncovered(reflect.TypeFor[staffapi.StrictServerInterface]()); len(bad) > 0 {
		t.Fatal(bad)
	}
}
