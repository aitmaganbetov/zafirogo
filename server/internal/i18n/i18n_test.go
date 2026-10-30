package i18n

import (
	"os"
	"reflect"
	"testing"
)

func TestParsePHP(t *testing.T) {
	src := []byte(`<?php
// comment
return [
    '*' => [
        'Создан товар: :product' => 'Тауар құрылды: :product',
        'It\'s' => 'a \\ b',
    ],
    'validation' => [
        'required' => 'Поле «:attribute» обязательно.',
        'attributes' => ['value' => 'значение'],
    ],
];`)
	got, err := ParsePHP(src)
	if err != nil {
		t.Fatal(err)
	}
	want := Shipped{
		"*":          {"Создан товар: :product": "Тауар құрылды: :product", "It's": `a \ b`},
		"validation": {"required": "Поле «:attribute» обязательно.", "attributes.value": "значение"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%#v", got)
	}
}

// Файлы старого проекта разбираются целиком (если репозиторий рядом).
func TestParseShippedFiles(t *testing.T) {
	for _, code := range []string{"ru", "kk", "en"} {
		src, err := os.ReadFile("../../../../Zafiro/database/translations/" + code + ".php")
		if err != nil {
			t.Skip("Zafiro рядом не найден")
		}
		s, err := ParsePHP(src)
		if err != nil {
			t.Fatalf("%s: %v", code, err)
		}
		if len(s["*"]) < 1000 || len(s["validation"]) == 0 || len(s["agent"]) == 0 {
			t.Fatalf("%s: groups %d/%d/%d", code, len(s["*"]), len(s["validation"]), len(s["agent"]))
		}
	}
}

func TestEntries(t *testing.T) {
	cases := []struct {
		name, locale, key, value string
		want                     map[string]string
	}{
		{"placeholder", "kk", "Создан товар: :product", "Тауар құрылды: :product",
			map[string]string{"Создан товар: {{product}}": "Тауар құрылды: {{product}}"}},
		{"time not placeholder", "ru", "Смена в 10:30", "Смена в 10:30", map[string]string{"Смена в 10:30": "Смена в 10:30"}},
		{"ru plural", "ru", ":count минута|:count минуты|:count минут", ":count минута|:count минуты|:count минут",
			map[string]string{
				"{{count}} минута|{{count}} минуты|{{count}} минут":       "{{count}} минута",
				"{{count}} минута|{{count}} минуты|{{count}} минут_one":   "{{count}} минута",
				"{{count}} минута|{{count}} минуты|{{count}} минут_few":   "{{count}} минуты",
				"{{count}} минута|{{count}} минуты|{{count}} минут_many":  "{{count}} минут",
				"{{count}} минута|{{count}} минуты|{{count}} минут_other": "{{count}} минут",
			}},
		{"kk one form", "kk", ":count минута|:count минуты|:count минут", ":count минут",
			map[string]string{
				"{{count}} минута|{{count}} минуты|{{count}} минут":       "{{count}} минут",
				"{{count}} минута|{{count}} минуты|{{count}} минут_one":   "{{count}} минут",
				"{{count}} минута|{{count}} минуты|{{count}} минут_other": "{{count}} минут",
			}},
		{"en two forms with condition", "en", ":count товар|:count товара|:count товаров", "{1} :count item|[2,*] :count items",
			map[string]string{
				"{{count}} товар|{{count}} товара|{{count}} товаров":       "{{count}} item",
				"{{count}} товар|{{count}} товара|{{count}} товаров_one":   "{{count}} item",
				"{{count}} товар|{{count}} товара|{{count}} товаров_other": "{{count}} items",
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Entries(c.locale, c.key, c.value); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("%v", got)
			}
		})
	}
}

func TestHashKey(t *testing.T) {
	// sha1('*|Касса') — Translation::hashKey('*', 'Касса').
	if got := HashKey("*", "Касса"); got != "1227424f2b68053f0f1e6f5b9203c4648764e9e2" {
		t.Fatal(got)
	}
	if HashKey("*", "a") == HashKey("agent", "a") {
		t.Fatal("group must change hash")
	}
}

func TestPluralIndexRu(t *testing.T) {
	for n, want := range map[int]int{1: 0, 21: 0, 11: 2, 2: 1, 4: 1, 12: 2, 14: 2, 22: 1, 5: 2, 0: 2, 111: 2, 101: 0} {
		if got := pluralIndex("ru", n); got != want {
			t.Errorf("%d: %d, want %d", n, got, want)
		}
	}
}
