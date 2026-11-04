package httpx

import (
	"context"
	"errors"
	"net/http"
	"testing"

	apicommon "github.com/aitmaganbetov/zafirogo/server/internal/http/api/common"
)

type fakeCatalog map[string]string

func (f fakeCatalog) Lookup(_ context.Context, _ string, key string) (string, bool) {
	v, ok := f[key]
	return v, ok
}

// F1-5: тексты ошибок переводятся на язык клуба — ключ + подстановки, правила валидатора, текст-ключ.
func TestLocalizedErrors(t *testing.T) {
	kk := fakeCatalog{
		"Недостаточно товара: :product.": "Тауар жеткіліксіз: :product.",
		"Смена уже открыта.":             "Ауысым ашық.",
		"validation.required":            "«:attribute» өрісі міндетті.",
		"validation.attributes.amount":   "сома",
		"validation.max.string":          "«:attribute» :max таңбадан аспауы керек.",
	}
	ctx := context.Background()

	e := ValidationMsg("items", "Недостаточно товара: :product.", map[string]string{"product": "Кола"})
	got := e.localized(ctx, kk, "kk").Body.(apicommon.ValidationError)
	if got.Message != "Тауар жеткіліксіз: Кола." || got.Errors["items"][0] != got.Message {
		t.Fatalf("msg: %+v", got)
	}
	// исходная ошибка не меняется
	if e.Body.(apicommon.ValidationError).Message != "Недостаточно товара: Кола." {
		t.Fatal("original mutated")
	}

	r := RuleError("amount", "required").localized(ctx, kk, "kk").Body.(apicommon.ValidationError)
	if r.Message != "«сома» өрісі міндетті." {
		t.Fatalf("rule: %q", r.Message)
	}
	var f FormErrors
	f.Add("name", RuleMessage("name", "max_string", "255"))
	var ae *APIError
	if !errors.As(f.Err(), &ae) {
		t.Fatal("form error")
	}
	fe := ae.localized(ctx, kk, "kk").Body.(apicommon.ValidationError)
	if fe.Errors["name"][0] != "«name» 255 таңбадан аспауы керек." {
		t.Fatalf("form: %q", fe.Errors["name"][0])
	}

	plain := NewAPIError(http.StatusUnprocessableEntity, "x", "Смена уже открыта.").localized(ctx, kk, "kk").Body.(apicommon.Error)
	if *plain.Message != "Ауысым ашық." {
		t.Fatalf("plain: %q", *plain.Message)
	}
	// нет перевода — текст без изменений
	none := ErrorMsg(http.StatusConflict, "x", "Нет: :n", map[string]string{"n": "1"}).localized(ctx, kk, "kk").Body.(apicommon.Error)
	if *none.Message != "Нет: 1" {
		t.Fatalf("none: %q", *none.Message)
	}
}
