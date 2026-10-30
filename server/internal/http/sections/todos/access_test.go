package todos

import (
	"reflect"
	"testing"

	todosapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/todos"
)

// Каждая операция раздела либо публичная (проверка в обработчике), либо закрыта правом маршрута Laravel.
func TestEveryOperationHasRight(t *testing.T) {
	if bad := access.Uncovered(reflect.TypeFor[todosapi.StrictServerInterface]()); len(bad) > 0 {
		t.Fatal(bad)
	}
}
