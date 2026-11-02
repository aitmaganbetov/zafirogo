package i18n

// Проверка переводов (Этап 4, H4): каждый ключ интерфейса — t('…') фронта (web/src) и русский текст сервера
// (ошибки API, сообщения, подписи журнала) — переведён на ru/kk/en: либо в database/translations старого проекта,
// либо в db/seed/translations-new.{ru,kk,en}.json. Плюс правила самих новых переводов: те же подстановки
// :name, что в ключе, формы множественного числа ru — 3, kk — 1, en — 2.
//
// Старый проект: $ZAFIRO_LEGACY_TRANSLATIONS или ../Zafiro рядом с репозиторием; без него тест пропускается,
// кроме CI (ZAFIRO_REQUIRE_LEGACY=1 — job translations в .github/workflows/go.yml забирает старый репозиторий).

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const repoRoot = "../../.."

var (
	cyrillicRe = regexp.MustCompile(`[А-Яа-яЁё]`)
	// tCallRe — t('ключ') фронта (ключи — только литералы в одинарных кавычках, как принято в web/src).
	tCallRe = regexp.MustCompile(`\bt\(\s*'((?:\\.|[^'\\\n])*)'`)
	// i18nextVarRe — {{name}} → :name (ключи в БД — в формате Laravel, решение Q-065).
	i18nextVarRe = regexp.MustCompile(`\{\{\s*(\w+)\s*\}\}`)
	// fragmentRe — кусок склеиваемой строки или служебный текст, а не ключ перевода: пробел или знак по краям,
	// форматные глаголы fmt, разметка документации, префикс ошибки Go («money: …»).
	fragmentRe   = regexp.MustCompile(`^\s|\s$|[«№#]$|^[,.;:)(—]|%[sdvwqTx]|[<{\[]|^[a-z_.]+:`)
	laravelVarRe = regexp.MustCompile(`:([a-z_]+)`)
)

// serverSkipDirs — не интерфейс: команды, конфигурация и инструмент переноса данных (тексты для оператора),
// сгенерированный код.
var serverSkipDirs = []string{"cmd", "gen", "internal/store", "internal/config", "internal/app", "internal/http/api", "internal/migrate"}

// serverSkipFiles — файлы со своей локализацией: «5 минут назад» на ru/kk/en (Carbon diffForHumans).
var serverSkipFiles = []string{
	"internal/http/sections/notifications/humantime.go",
	"internal/http/sections/superadmin/humantime.go",
}

// notKeys — русские литералы сервера, которые не переводятся: маркеры в данных и сравнениях (CLAUDE.md
// «Локализация»: данные и маркеры через t() не пропускать), названия языков, описания для разработчика.
var notKeys = map[string]string{
	"Продажа POS":             "cash: маркер типа операции в данных",
	"Возврат POS":             "cash: маркер типа операции в данных",
	"Заказ из агента":         "shop: AgentShop::STOCK_NOTE, маркер движения склада",
	"Отмена заказа из агента": "shop: AgentShop::CANCEL_NOTE, маркер движения склада",
	"Недостаточно":            "guestapi: поиск подстроки в тексте ошибки",
	"бонус":                   "guestapi: поиск подстроки в типе операции",
	"сек":                     "settings: разбор «30 сек» старых настроек",
	"минута":                  "billing: слово описания журнала (данные)",
	"минуты":                  "billing: слово описания журнала (данные)",
	"минут":                   "billing: слово описания журнала (данные)",
	"Русский":                 "i18n: название языка (данные languages)",
	"Қазақша":                 "i18n: название языка (данные languages)",
	"МЕТОД /путь":             "httpx: описание формата",
	"Вебхуки интеграций: событие → строки webhook_deliveries": "webhook: описание консьюмера NATS",
}

func toLaravel(key string) string { return i18nextVarRe.ReplaceAllString(key, ":$1") }

// webKeys — ключи t('…') фронта в формате Laravel → файл.
func webKeys(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	root := filepath.Join(repoRoot, "web", "src")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() || !strings.HasSuffix(name, ".ts") && !strings.HasSuffix(name, ".tsx") ||
			strings.Contains(name, ".test.") || strings.HasSuffix(name, ".gen.ts") {
			return nil
		}
		src, err := os.ReadFile(p) // #nosec G304 -- файлы репозитория
		if err != nil {
			return err
		}
		for _, m := range tCallRe.FindAllStringSubmatch(string(src), -1) {
			k := strings.NewReplacer(`\'`, `'`, `\\`, `\`).Replace(m[1])
			out[toLaravel(k)] = p
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) < 500 {
		t.Fatalf("web keys: %d — неверный путь к web/src?", len(out))
	}
	return out
}

// serverKeys — русские строковые литералы Go (go/ast, без комментариев), кроме фрагментов, маркеров и служебных.
func serverKeys(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	root := filepath.Join(repoRoot, "server")
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if slices.Contains(serverSkipDirs, rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") || strings.HasSuffix(rel, ".gen.go") ||
			slices.Contains(serverSkipFiles, rel) {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, err := strconv.Unquote(lit.Value)
			if err != nil || !cyrillicRe.MatchString(v) || strings.Contains(v, "\n") || fragmentRe.MatchString(v) {
				return true
			}
			if _, skip := notKeys[v]; !skip {
				out[v] = rel
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func legacyDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("ZAFIRO_LEGACY_TRANSLATIONS")
	if dir == "" {
		dir = filepath.Join(repoRoot, "..", "Zafiro", "database", "translations")
	}
	if _, err := os.Stat(filepath.Join(dir, "ru.php")); err != nil {
		if os.Getenv("ZAFIRO_REQUIRE_LEGACY") != "" {
			t.Fatalf("нет переводов старого проекта: %s", dir)
		}
		t.Skip("переводы старого проекта не найдены (ZAFIRO_LEGACY_TRANSLATIONS)")
	}
	return dir
}

// flat — ключи языка: группа '*' как есть, остальные — «группа.ключ».
func flat(s Shipped) map[string]string {
	out := map[string]string{}
	for g, lines := range s {
		for k, v := range lines {
			if g == "*" {
				out[k] = v
			} else {
				out[g+"."+k] = v
			}
		}
	}
	return out
}

func TestEveryKeyTranslated(t *testing.T) {
	dir := legacyDir(t)
	seed, err := LoadSeed(filepath.Join(repoRoot, "db", "seed"))
	if err != nil {
		t.Fatal(err)
	}
	langs := map[string]map[string]string{}
	for _, l := range DefaultLanguages {
		src, err := os.ReadFile(filepath.Join(dir, l.Code+".php")) // #nosec G304 -- путь из окружения теста
		if err != nil {
			t.Fatal(err)
		}
		old, err := ParsePHP(src)
		if err != nil {
			t.Fatal(err)
		}
		langs[l.Code] = flat(old.Merge(seed[l.Code]))
	}
	keys := webKeys(t)
	for k, f := range serverKeys(t) {
		if _, ok := keys[k]; !ok {
			keys[k] = f
		}
	}
	var missing []string
	for k, f := range keys {
		var no []string
		for _, l := range DefaultLanguages {
			if v, ok := langs[l.Code][k]; !ok || v == "" {
				no = append(no, l.Code)
			}
		}
		if len(no) > 0 {
			missing = append(missing, strings.Join(no, ",")+"\t"+f+"\t"+k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("%d ключей без перевода — добавьте в db/seed/translations-new.{ru,kk,en}.json (языки, файл, ключ):\n%s",
			len(missing), strings.Join(missing, "\n"))
	}
}

// Новые переводы: тот же набор ключей во всех языках, ru = ключ, подстановки ключа сохранены, число форм
// множественного числа — ru 3, kk 1, en 2 (CLAUDE.md «Локализация»).
func TestSeedTranslations(t *testing.T) {
	seed, err := LoadSeed(filepath.Join(repoRoot, "db", "seed"))
	if err != nil {
		t.Fatal(err)
	}
	ru := flat(seed["ru"])
	if len(ru) == 0 {
		t.Fatal("нет db/seed/translations-new.ru.json")
	}
	forms := map[string]int{"ru": 3, "kk": 1, "en": 2}
	for _, l := range DefaultLanguages {
		lines := flat(seed[l.Code])
		if len(lines) != len(ru) {
			t.Errorf("%s: %d строк, в ru — %d", l.Code, len(lines), len(ru))
		}
		for k, v := range lines {
			if _, ok := ru[k]; !ok {
				t.Errorf("%s: ключа %q нет в ru", l.Code, k)
				continue
			}
			if l.Code == "ru" && v != k {
				t.Errorf("ru: значение %q ≠ ключ %q", v, k)
			}
			if strings.TrimSpace(v) == "" {
				t.Errorf("%s: пустой перевод %q", l.Code, k)
			}
			if strings.Contains(k, "|") {
				if n := len(strings.Split(v, "|")); n != forms[l.Code] {
					t.Errorf("%s: %q — %d форм, нужно %d", l.Code, k, n, forms[l.Code])
				}
			}
			want := laravelVarRe.FindAllString(k, -1)
			for _, p := range want {
				if !laravelVarRe.MatchString(v) || !strings.Contains(v, p) {
					t.Errorf("%s: %q — нет подстановки %s в %q", l.Code, k, p, v)
				}
			}
		}
	}
}
