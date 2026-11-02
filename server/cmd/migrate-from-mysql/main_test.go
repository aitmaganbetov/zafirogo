package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunUsage(t *testing.T) {
	t.Setenv("ZAFIRO_MIGRATE_MYSQL_DSN", "")
	t.Setenv("ZAFIRO_MIGRATE_PG_DSN", "")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "usage"},
		{[]string{"migrate"}, "usage"},
		{[]string{"plan"}, "нужны --club"},
		{[]string{"apply", "--club", "1"}, "нужны --club"},
		{[]string{"plan", "--club", "1", "--mysql", "x", "--pg", "y", "--source-tz", "Mars/Olympus"}, "--source-tz"},
	} {
		var out, errOut bytes.Buffer
		if code := run(tc.args, &out, &errOut); code != exitUsage {
			t.Errorf("%v: код %d, ожидали %d", tc.args, code, exitUsage)
		}
		if !strings.Contains(errOut.String(), tc.want) {
			t.Errorf("%v: %q без %q", tc.args, errOut.String(), tc.want)
		}
	}
}
