// Команда import-translations — перенос переводов интерфейса старого проекта в Postgres, как
// `php artisan translations:sync` (Laravel: app/Console/Commands/SyncTranslations.php, TranslationCatalog::sync()):
// языки ru/kk/en (если их нет) и строки из database/translations/{code}.php, которых ещё нет в таблице.
// Отредактированные в суперадминке строки не меняются. Строки хранятся в формате Laravel (":name", "a|b|c");
// в формат i18next их переводит GET /api/i18n/{locale} (решение Q-065).
//
//	go run ./cmd/import-translations -dir ../../Zafiro/database/translations -dsn "$ZAFIRO_DATABASE_PLATFORM_URL"
//
// DSN — логин zafiro_platform или владелец схемы (переменная окружения IMPORT_TRANSLATIONS_DSN, если -dsn пуст).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/i18n"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "import-translations:", err)
		os.Exit(1)
	}
}

func run() error {
	dir := flag.String("dir", "", "каталог database/translations старого проекта ({ru,kk,en}.php)")
	dsn := flag.String("dsn", "", "DSN Postgres (иначе IMPORT_TRANSLATIONS_DSN)")
	dry := flag.Bool("dry-run", false, "только разобрать файлы и показать число строк")
	flag.Parse()
	if *dir == "" {
		return errors.New("-dir обязателен")
	}
	if *dsn == "" {
		*dsn = os.Getenv("IMPORT_TRANSLATIONS_DSN")
	}

	shipped := map[string]i18n.Shipped{}
	for _, l := range i18n.DefaultLanguages {
		src, err := os.ReadFile(filepath.Join(*dir, l.Code+".php")) // #nosec G304 -- путь задаёт оператор
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		s, err := i18n.ParsePHP(src)
		if err != nil {
			return fmt.Errorf("%s.php: %w", l.Code, err)
		}
		shipped[l.Code] = s
		n := 0
		for _, g := range s {
			n += len(g)
		}
		fmt.Printf("%s.php: %d строк\n", l.Code, n)
	}
	if len(shipped) == 0 {
		return errors.New("в каталоге нет файлов переводов")
	}
	if *dry {
		return nil
	}
	if *dsn == "" {
		return errors.New("нужен -dsn или IMPORT_TRANSLATIONS_DSN")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := db.NewPool(ctx, *dsn)
	if err != nil {
		return errors.New("не удалось подключиться к Postgres (проверьте DSN)")
	}
	defer pool.Close()
	inserted, err := i18n.Import(ctx, pool, shipped)
	if err != nil {
		return err
	}
	fmt.Printf("добавлено строк: %d\n", inserted)
	return nil
}
