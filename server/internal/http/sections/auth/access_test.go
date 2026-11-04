package auth

import (
	"reflect"
	"testing"

	authapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/auth"
)

// Каждая операция раздела либо публичная (проверка в обработчике), либо закрыта правом маршрута Laravel.
func TestEveryOperationHasRight(t *testing.T) {
	if bad := access.Uncovered(reflect.TypeFor[authapi.StrictServerInterface]()); len(bad) > 0 {
		t.Fatal(bad)
	}
}
