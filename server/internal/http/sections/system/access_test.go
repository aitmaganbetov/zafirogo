package system

import (
	"reflect"
	"testing"

	systemapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/system"
)

// Каждая операция раздела либо публичная (проверка в обработчике), либо закрыта правом маршрута Laravel.
func TestEveryOperationHasRight(t *testing.T) {
	if bad := access.Uncovered(reflect.TypeFor[systemapi.StrictServerInterface]()); len(bad) > 0 {
		t.Fatal(bad)
	}
}
