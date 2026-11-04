package recovery

import (
	"reflect"
	"testing"

	recoveryapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/recovery"
)

// Каждая операция раздела либо публичная (проверка в обработчике), либо закрыта правом маршрута Laravel.
func TestEveryOperationHasRight(t *testing.T) {
	if bad := access.Uncovered(reflect.TypeFor[recoveryapi.StrictServerInterface]()); len(bad) > 0 {
		t.Fatal(bad)
	}
}
