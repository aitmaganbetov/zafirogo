// Команда zafiro — единый бинарник сервера: REST API админки, (позже) gRPC для агентов,
// воркеры и вшитый фронтенд.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aitmaganbetov/zafirogo/server/internal/config"
	httpserver "github.com/aitmaganbetov/zafirogo/server/internal/http"
	"github.com/aitmaganbetov/zafirogo/server/internal/logging"
	"github.com/aitmaganbetov/zafirogo/server/internal/webui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "zafiro:", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "", "путь к YAML-файлу конфигурации (иначе "+config.FileEnv+")")
	showVersion := flag.Bool("version", false, "напечатать версию и выйти")
	flag.Parse()

	if *showVersion {
		fmt.Println(version())
		return nil
	}

	cfg, err := config.Load(*configPath, nil)
	if err != nil {
		return err
	}
	logger := logging.New(cfg.Log, os.Stdout)
	slog.SetDefault(logger)
	logger.Info("starting zafiro", slog.String("version", version()), slog.Any("config", cfg))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var checkers []httpserver.Checker
	if cfg.Database.URL != "" {
		pool, err := pgxpool.New(ctx, cfg.Database.URL)
		if err != nil {
			// Ошибка разбора DSN может содержать пароль — не выводим её текст.
			return errors.New("database: некорректный ZAFIRO_DATABASE_URL")
		}
		defer pool.Close()
		checkers = append(checkers, httpserver.CheckFunc{CheckName: "postgres", Fn: pool.Ping})
	}

	trusted, err := cfg.HTTP.TrustedPrefixes()
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr: cfg.HTTP.Addr,
		Handler: httpserver.NewRouter(httpserver.Options{
			Logger:         logger,
			RequestTimeout: cfg.HTTP.RequestTimeout,
			TrustedProxies: trusted,
			Checkers:       checkers,
			Frontend:       webui.Handler(webui.FS()),
		}),
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
		MaxHeaderBytes:    cfg.HTTP.MaxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("http listening", slog.String("addr", cfg.HTTP.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err, ok := <-errCh:
		if ok {
			return fmt.Errorf("http: %w", err)
		}
		return nil
	case <-ctx.Done():
	}

	logger.Info("shutting down", slog.Duration("timeout", cfg.ShutdownTimeout))
	// Новый контекст: ctx уже отменён сигналом.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	logger.Info("stopped")
	return nil
}

func version() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	rev, dirty := "", ""
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	if rev == "" {
		return bi.Main.Version + " " + bi.GoVersion
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	return rev + dirty + " " + bi.GoVersion
}
