package i18n

import (
	"reflect"
	"testing"

	i18napi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/i18n"
)

// Каждая операция раздела либо публичная (проверка в обработчике), либо закрыта правом маршрута Laravel.
func TestEveryOperationHasRight(t *testing.T) {
	if bad := access.Uncovered(reflect.TypeFor[i18napi.StrictServerInterface]()); len(bad) > 0 {
		t.Fatal(bad)
	}
}
