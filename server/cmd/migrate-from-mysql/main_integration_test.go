//go:build integration

package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/migrate/migratetest"
)

// Демо-клуб переносится одной командой: plan → apply → verify, повторный apply — код «уже перенесён».
func TestCommandDemoClub(t *testing.T) {
	_, mysqlDSN := migratetest.StartMySQL(t)
	pg := dbtest.New(t)
	t.Setenv("ZAFIRO_MIGRATE_MYSQL_DSN", mysqlDSN)
	t.Setenv("ZAFIRO_MIGRATE_PG_DSN", pg.Owner.Config().ConnString())
	report := filepath.Join(t.TempDir(), "report.json")

	for _, step := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"plan", "--club", "zafiro-arena"}, exitOK, "Итого: блокеров 0"},
		{[]string{"apply", "--club", "zafiro-arena", "--json", report}, exitOK, "записано"},
		{[]string{"verify", "--club", "1"}, exitOK, "касса: остаток открытой смены"},
		{[]string{"apply", "--club", "1"}, exitMigrated, "клуб уже есть в zafirogo"},
	} {
		var out, errOut bytes.Buffer
		if code := run(step.args, &out, &errOut); code != step.code {
			t.Fatalf("%v: код %d, ожидали %d\n%s\n%s", step.args, code, step.code, out.String(), errOut.String())
		}
		if !strings.Contains(out.String(), step.want) {
			t.Fatalf("%v: в отчёте нет %q\n%s", step.args, step.want, out.String())
		}
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"verify", "--club", "zafiro-lite"}, &out, &errOut); code != exitMismatch {
		t.Fatalf("verify неперенесённого клуба: код %d\n%s", code, out.String())
	}
}
