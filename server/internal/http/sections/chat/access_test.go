package chat

import (
	"reflect"
	"testing"

	chatapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/chat"
)

// Каждая операция раздела либо публичная (проверка в обработчике), либо закрыта правом маршрута Laravel.
func TestEveryOperationHasRight(t *testing.T) {
	if bad := access.Uncovered(reflect.TypeFor[chatapi.StrictServerInterface]()); len(bad) > 0 {
		t.Fatal(bad)
	}
}
