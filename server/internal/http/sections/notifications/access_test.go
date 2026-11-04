package notifications

import (
	"reflect"
	"testing"

	notificationsapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/notifications"
)

// Каждая операция раздела либо публичная (проверка в обработчике), либо закрыта правом маршрута Laravel.
func TestEveryOperationHasRight(t *testing.T) {
	if bad := access.Uncovered(reflect.TypeFor[notificationsapi.StrictServerInterface]()); len(bad) > 0 {
		t.Fatal(bad)
	}
}
