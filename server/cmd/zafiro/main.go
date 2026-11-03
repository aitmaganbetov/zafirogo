// Команда zafiro — единый бинарник сервера: REST API и WebSocket админки, gRPC агентов (mTLS),
// воркеры и вшитый фронтенд. Сборка подсистем — server/internal/app; миграции — goose отдельно
// (task db:migrate).
//
// Подкоманды обслуживания (та же конфигурация, что у сервера):
//
//	zafiro [-config файл] superadmin-totp-reset -phone +7XXXXXXXXXX   аварийный сброс второго фактора суперадмина
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"syscall"

	"github.com/aitmaganbetov/zafirogo/server/internal/app"
	"github.com/aitmaganbetov/zafirogo/server/internal/config"
	"github.com/aitmaganbetov/zafirogo/server/internal/logging"
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
	if flag.NArg() > 0 {
		return runSubcommand(*configPath, flag.Arg(0), flag.Args()[1:])
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

	a, err := app.Build(ctx, cfg, logger)
	if err != nil {
		return err
	}
	return a.Run(ctx)
}

// runSubcommand — подкоманды обслуживания (не сервер).
func runSubcommand(configPath, name string, args []string) error {
	if name != cmdTOTPReset {
		return fmt.Errorf("неизвестная подкоманда %q (есть: %s)", name, cmdTOTPReset)
	}
	cfg, err := config.Load(configPath, nil)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runTOTPReset(ctx, cfg, args, os.Stdout)
}

// buildVersion — версия сборки образа (Dockerfile: -ldflags "-X main.buildVersion=<тег или коммит>"); в образе нет
// .git, поэтому сведения VCS из debug.ReadBuildInfo там пусты.
var buildVersion string

func version() string {
	if buildVersion != "" {
		return buildVersion + " " + runtime.Version()
	}
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
