// Package i18n — переводы интерфейса: импорт файлов database/translations/{code}.php старого проекта в таблицу
// translations (как TranslationCatalog::sync()) и пакеты для i18next (GET /api/i18n/{locale}) с конвертацией
// формата Laravel (":name", "a|b|c" trans_choice) в формат i18next ("{{name}}", ключи _one/_few/_many/_other).
//
// В БД строки хранятся в формате Laravel (как в старом проекте и в редакторе суперадминки — решение Q-065);
// конвертация — при выдаче фронту.
package i18n

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Shipped — строки одного языка: группа → ключ → текст. Группа '*' — JSON-фразы (ключ = русский текст),
// остальные группы (validation, pagination, agent) — «плоские» ключи через точку (Arr::dot()).
type Shipped map[string]map[string]string

// ParsePHP разбирает файл переводов вида `<?php return ['*' => ['ключ' => 'текст', …], 'validation' => [...]];`
// (TranslationCatalog::shipped()). Поддерживаются строки в одинарных и двойных кавычках, вложенные массивы и
// комментарии // и /* */. Произвольный PHP-код не исполняется.
func ParsePHP(src []byte) (Shipped, error) {
	p := &phpParser{s: string(src)}
	p.skipSpace()
	if !strings.HasPrefix(p.s[p.i:], "<?php") {
		return nil, errors.New("i18n: no <?php")
	}
	p.i += len("<?php")
	p.skipSpace()
	if !strings.HasPrefix(p.s[p.i:], "return") {
		return nil, errors.New("i18n: expected return")
	}
	p.i += len("return")
	v, err := p.value()
	if err != nil {
		return nil, err
	}
	root, ok := v.(phpArray)
	if !ok {
		return nil, errors.New("i18n: root is not an array")
	}
	out := Shipped{}
	for _, kv := range root {
		lines, ok := kv.v.(phpArray)
		if !ok {
			return nil, fmt.Errorf("i18n: group %q is not an array", kv.k)
		}
		g := map[string]string{}
		if kv.k == "*" {
			for _, l := range lines {
				s, ok := l.v.(string)
				if !ok {
					return nil, fmt.Errorf("i18n: '*' line %q is not a string", l.k)
				}
				g[l.k] = s
			}
		} else {
			dot(g, "", lines)
		}
		out[kv.k] = g
	}
	return out, nil
}

// Keys — ключи группы по порядку (для детерминированного импорта).
func (s Shipped) Keys(group string) []string {
	keys := make([]string, 0, len(s[group]))
	for k := range s[group] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Groups — группы по порядку.
func (s Shipped) Groups() []string {
	gs := make([]string, 0, len(s))
	for g := range s {
		gs = append(gs, g)
	}
	sort.Strings(gs)
	return gs
}

// dot — Arr::dot(): вложенные массивы в ключи через точку.
func dot(out map[string]string, prefix string, a phpArray) {
	for _, kv := range a {
		key := kv.k
		if prefix != "" {
			key = prefix + "." + kv.k
		}
		switch v := kv.v.(type) {
		case string:
			out[key] = v
		case phpArray:
			dot(out, key, v)
		}
	}
}

type phpKV struct {
	k string
	v any // string | phpArray
}

type phpArray []phpKV

type phpParser struct {
	s string
	i int
}

func (p *phpParser) errf(format string, a ...any) error {
	line := 1 + strings.Count(p.s[:p.i], "\n")
	return fmt.Errorf("i18n: line %d: %s", line, fmt.Sprintf(format, a...))
}

func (p *phpParser) skipSpace() {
	for p.i < len(p.s) {
		switch {
		case strings.ContainsRune(" \t\r\n", rune(p.s[p.i])):
			p.i++
		case strings.HasPrefix(p.s[p.i:], "//") || strings.HasPrefix(p.s[p.i:], "#"):
			if j := strings.IndexByte(p.s[p.i:], '\n'); j >= 0 {
				p.i += j + 1
			} else {
				p.i = len(p.s)
			}
		case strings.HasPrefix(p.s[p.i:], "/*"):
			if j := strings.Index(p.s[p.i+2:], "*/"); j >= 0 {
				p.i += j + 4
			} else {
				p.i = len(p.s)
			}
		default:
			return
		}
	}
}

func (p *phpParser) value() (any, error) {
	p.skipSpace()
	if p.i >= len(p.s) {
		return nil, p.errf("unexpected end")
	}
	switch p.s[p.i] {
	case '[':
		return p.array()
	case '\'', '"':
		return p.str()
	}
	return nil, p.errf("unexpected %q", p.s[p.i])
}

func (p *phpParser) array() (phpArray, error) {
	p.i++ // [
	var out phpArray
	for {
		p.skipSpace()
		if p.i >= len(p.s) {
			return nil, p.errf("unterminated array")
		}
		if p.s[p.i] == ']' {
			p.i++
			return out, nil
		}
		k, err := p.str()
		if err != nil {
			return nil, err
		}
		p.skipSpace()
		if !strings.HasPrefix(p.s[p.i:], "=>") {
			return nil, p.errf("expected =>")
		}
		p.i += 2
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		out = append(out, phpKV{k: k, v: v})
		p.skipSpace()
		if p.i < len(p.s) && p.s[p.i] == ',' {
			p.i++
		}
	}
}

// str — строка PHP: в одинарных кавычках экранируются только \' и \\; в двойных — \n, \t, \\, \", \$.
func (p *phpParser) str() (string, error) {
	p.skipSpace()
	if p.i >= len(p.s) || (p.s[p.i] != '\'' && p.s[p.i] != '"') {
		return "", p.errf("expected string")
	}
	q := p.s[p.i]
	p.i++
	var b strings.Builder
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch {
		case c == q:
			p.i++
			return b.String(), nil
		case c == '\\' && p.i+1 < len(p.s):
			n := p.s[p.i+1]
			switch {
			case n == q || n == '\\':
				b.WriteByte(n)
				p.i += 2
				continue
			case q == '"' && n == 'n':
				b.WriteByte('\n')
				p.i += 2
				continue
			case q == '"' && n == 't':
				b.WriteByte('\t')
				p.i += 2
				continue
			case q == '"' && n == '$':
				b.WriteByte('$')
				p.i += 2
				continue
			}
			b.WriteByte(c)
			p.i++
		case q == '"' && c == '$':
			return "", p.errf("interpolation in double-quoted string is not supported")
		default:
			b.WriteByte(c)
			p.i++
		}
	}
	return "", p.errf("unterminated string")
}
