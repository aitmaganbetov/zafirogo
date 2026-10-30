package pclogin

import (
	"reflect"
	"testing"

	pcloginapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/pclogin"
)

// Каждая операция раздела либо публичная (проверка в обработчике), либо закрыта правом маршрута Laravel.
func TestEveryOperationHasRight(t *testing.T) {
	if bad := access.Uncovered(reflect.TypeFor[pcloginapi.StrictServerInterface]()); len(bad) > 0 {
		t.Fatal(bad)
	}
}
