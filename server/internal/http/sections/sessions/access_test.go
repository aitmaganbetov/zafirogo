package sessions

import (
	"reflect"
	"testing"

	sessionsapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/sessions"
)

// Каждая операция раздела либо публичная (проверка в обработчике), либо закрыта правом маршрута Laravel.
func TestEveryOperationHasRight(t *testing.T) {
	if bad := access.Uncovered(reflect.TypeFor[sessionsapi.StrictServerInterface]()); len(bad) > 0 {
		t.Fatal(bad)
	}
}
