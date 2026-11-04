package clubmap

import (
	"reflect"
	"testing"

	clubmapapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/clubmap"
)

// Каждая операция раздела либо публичная (проверка в обработчике), либо закрыта правом маршрута Laravel.
func TestEveryOperationHasRight(t *testing.T) {
	if bad := access.Uncovered(reflect.TypeFor[clubmapapi.StrictServerInterface]()); len(bad) > 0 {
		t.Fatal(bad)
	}
}
