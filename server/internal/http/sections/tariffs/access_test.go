package tariffs

import (
	"reflect"
	"testing"

	tariffsapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/tariffs"
)

// Каждая операция раздела либо публичная (проверка в обработчике), либо закрыта правом маршрута Laravel.
func TestEveryOperationHasRight(t *testing.T) {
	if bad := access.Uncovered(reflect.TypeFor[tariffsapi.StrictServerInterface]()); len(bad) > 0 {
		t.Fatal(bad)
	}
}
