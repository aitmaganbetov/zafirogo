package cash

import (
	"reflect"
	"testing"

	cashapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/cash"
)

// Каждая операция раздела либо публичная (проверка в обработчике), либо закрыта правом маршрута Laravel.
func TestEveryOperationHasRight(t *testing.T) {
	if bad := access.Uncovered(reflect.TypeFor[cashapi.StrictServerInterface]()); len(bad) > 0 {
		t.Fatal(bad)
	}
}
