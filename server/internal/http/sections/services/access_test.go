package services

import (
	"reflect"
	"testing"

	servicesapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/services"
)

// Каждая операция раздела либо публичная (проверка в обработчике), либо закрыта правом маршрута Laravel.
func TestEveryOperationHasRight(t *testing.T) {
	if bad := access.Uncovered(reflect.TypeFor[servicesapi.StrictServerInterface]()); len(bad) > 0 {
		t.Fatal(bad)
	}
}
