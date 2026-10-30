package i18n

import (
	"crypto/sha1" // #nosec G505 -- формат key_hash старого проекта (Translation::hashKey), не защита
	"encoding/hex"
	"regexp"
	"strings"
)

// HashKey — translations.key_hash: sha1("group|key") (Laravel: app/Models/Translation.php Translation::hashKey()).
func HashKey(group, key string) string {
	sum := sha1.Sum([]byte(group + "|" + key)) // #nosec G401 -- см. импорт
	return hex.EncodeToString(sum[:])
}

// placeholderRe — подстановка Laravel :name (makeReplacements: ключ — буквы, цифры, «_»; первая — буква или «_»).
var placeholderRe = regexp.MustCompile(`:([A-Za-z_][A-Za-z0-9_]*)`)

// Placeholders — ":name" → "{{name}}" (формат i18next; решение Q-065). Имя переносится как есть (варианты
// :Name / :NAME Laravel в каталоге не встречаются).
func Placeholders(s string) string {
	return placeholderRe.ReplaceAllString(s, "{{$1}}")
}

// conditionRe — явные условия форм trans_choice: "{0} нет", "[2,*] много" (MessageSelector::stripConditions).
var conditionRe = regexp.MustCompile(`^\s*(\{[^}]*\}|\[[^\]]*\])\s*`)

// IsPlural — строка trans_choice: формы через «|» и подстановка :count.
func IsPlural(key string) bool {
	return strings.Contains(key, "|") && strings.Contains(key, ":count")
}

// pluralIndex — MessageSelector::getPluralIndex() Laravel для языков проекта.
func pluralIndex(locale string, n int) int {
	switch locale {
	case "ru", "uk", "be":
		switch {
		case n%10 == 1 && n%100 != 11:
			return 0
		case n%10 >= 2 && n%10 <= 4 && (n%100 < 10 || n%100 >= 20):
			return 1
		default:
			return 2
		}
	default: // en, kk и прочие «n == 1 ? 0 : 1»
		if n == 1 {
			return 0
		}
		return 1
	}
}

// categories — категории Intl.PluralRules (i18next ≥ v21, суффиксы _one/_few/_many/_other) и число-представитель
// каждой категории, по которому Laravel выбирал форму.
var categories = map[string][]struct {
	suffix string
	n      int
}{
	"ru": {{"_one", 1}, {"_few", 2}, {"_many", 5}, {"_other", 5}},
	"kk": {{"_one", 1}, {"_other", 2}},
	"en": {{"_one", 1}, {"_other", 2}},
}

// Entries — строки пакета i18next для одной строки каталога (ключ и перевод в формате Laravel):
// ключ и текст с {{name}}; для trans_choice — ещё формы с суффиксами категорий языка locale. Форма — как у
// Laravel (MessageSelector::choose(): нет формы с таким номером — первая).
func Entries(locale, key, value string) map[string]string {
	k := Placeholders(key)
	out := map[string]string{k: Placeholders(value)}
	if !IsPlural(key) {
		return out
	}
	forms := strings.Split(value, "|")
	for i, f := range forms {
		forms[i] = Placeholders(conditionRe.ReplaceAllString(f, ""))
	}
	out[k] = forms[0]
	cats, ok := categories[locale]
	if !ok {
		cats = categories["en"]
	}
	for _, c := range cats {
		idx := pluralIndex(locale, c.n)
		if len(forms) == 1 || idx >= len(forms) {
			idx = 0
		}
		out[k+c.suffix] = forms[idx]
	}
	return out
}
