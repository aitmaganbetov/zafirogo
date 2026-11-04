package integrations

import (
	"reflect"
	"testing"

	integrationsapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/integrations"
)

// Каждая операция раздела либо публичная (проверка доступа в обработчике), либо закрыта правом маршрута Laravel.
func TestEveryOperationHasRight(t *testing.T) {
	if bad := access.Uncovered(reflect.TypeFor[integrationsapi.StrictServerInterface]()); len(bad) > 0 {
		t.Fatal(bad)
	}
}
