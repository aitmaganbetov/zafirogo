package i18n

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// SeedFile — имя файла переводов строк, которых нет в database/translations старого проекта (новые тексты Этапов
// 1–4): db/seed/translations-new.{code}.json, формат — Shipped в JSON ({"*": {"ключ": "текст"}, "группа": {…}}),
// тексты в формате Laravel (":name", формы "a|b|c": ru — 3, kk — 1, en — 2).
func SeedFile(dir, code string) string {
	return filepath.Join(dir, "translations-new."+code+".json")
}

// LoadSeed читает db/seed/translations-new.{code}.json для языков DefaultLanguages; нет файла — язык пропускается.
func LoadSeed(dir string) (map[string]Shipped, error) {
	out := map[string]Shipped{}
	for _, l := range DefaultLanguages {
		src, err := os.ReadFile(SeedFile(dir, l.Code)) // #nosec G304 -- каталог задаёт оператор
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var s Shipped
		if err := json.Unmarshal(src, &s); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(SeedFile(dir, l.Code)), err)
		}
		out[l.Code] = s
	}
	return out, nil
}

// Merge добавляет в s строки extra, которых в s нет (строки файла старого проекта не перекрываются).
func (s Shipped) Merge(extra Shipped) Shipped {
	if s == nil {
		s = Shipped{}
	}
	for g, lines := range extra {
		if s[g] == nil {
			s[g] = map[string]string{}
		}
		for k, v := range lines {
			if _, ok := s[g][k]; !ok {
				s[g][k] = v
			}
		}
	}
	return s
}
