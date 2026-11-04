package reservations

import (
	"reflect"
	"testing"

	reservationsapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/reservations"
)

// Каждая операция раздела либо публичная (проверка в обработчике), либо закрыта правом маршрута Laravel.
func TestEveryOperationHasRight(t *testing.T) {
	if bad := access.Uncovered(reflect.TypeFor[reservationsapi.StrictServerInterface]()); len(bad) > 0 {
		t.Fatal(bad)
	}
}
