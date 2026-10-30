package news

import (
	"reflect"
	"testing"

	newsapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/news"
)

// Каждая операция раздела либо публичная (проверка в обработчике), либо закрыта правом маршрута Laravel.
func TestEveryOperationHasRight(t *testing.T) {
	if bad := access.Uncovered(reflect.TypeFor[newsapi.StrictServerInterface]()); len(bad) > 0 {
		t.Fatal(bad)
	}
}
