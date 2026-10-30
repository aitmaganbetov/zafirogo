package settings

import (
	"reflect"
	"testing"

	settingsapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/settings"
)

// Каждая операция раздела либо публичная (проверка доступа в обработчике), либо закрыта правом маршрута Laravel.
func TestEveryOperationHasRight(t *testing.T) {
	if bad := access.Uncovered(reflect.TypeFor[settingsapi.StrictServerInterface]()); len(bad) > 0 {
		t.Fatal(bad)
	}
}
