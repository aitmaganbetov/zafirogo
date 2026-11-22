package password

import (
	_ "embed"
	"strings"
	"unicode/utf8"
)

// IsCommon — пароль из списка самых частых (утёкшие базы, раскладки клавиатуры, повтор одного символа). Проверяется
// только для новых паролей аккаунтов персонала клуба — регистрация владельца и аккаунт по приглашению в клуб (аудит
// SEC-8, ASVS V2.1.7; docs/DECISIONS.md «Пароли персонала (аудит 22.11.2026)»). Минимальная длина и правила старого
// кода (min:6) не меняются; гостям и смене пароля проверка не добавлена. Регистр букв не важен.
func IsCommon(pw string) bool {
	if pw == "" {
		return false
	}
	first, _ := utf8.DecodeRuneInString(pw)
	if strings.Trim(pw, string(first)) == "" {
		return true // «aaaaaa», «111111», …
	}
	_, ok := commonPasswords[strings.ToLower(pw)]
	return ok
}

// commonPasswords — common_passwords.txt: пароли не короче 6 символов (короче отклоняет min:6) из верхушки публичных
// списков самых частых паролей (в т. ч. русская раскладка и транслит) и очевидные для платформы слова. Строки с «#» —
// комментарии.
//
//go:embed common_passwords.txt
var commonPasswordsTxt string

var commonPasswords = func() map[string]struct{} {
	m := map[string]struct{}{}
	for _, line := range strings.Split(commonPasswordsTxt, "\n") {
		line = strings.ToLower(strings.TrimSpace(line))
		if line != "" && !strings.HasPrefix(line, "#") {
			m[line] = struct{}{}
		}
	}
	return m
}()
