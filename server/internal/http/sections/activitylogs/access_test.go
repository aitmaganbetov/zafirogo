package activitylogs

import (
	"reflect"
	"testing"

	activitylogsapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/activitylogs"
)

// Каждая операция раздела либо публичная (проверка в обработчике), либо закрыта правом маршрута Laravel.
func TestEveryOperationHasRight(t *testing.T) {
	if bad := access.Uncovered(reflect.TypeFor[activitylogsapi.StrictServerInterface]()); len(bad) > 0 {
		t.Fatal(bad)
	}
}
