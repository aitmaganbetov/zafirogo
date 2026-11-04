package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/config"
)

func TestNewJSON(t *testing.T) {
	var buf bytes.Buffer
	l := New(config.Log{Level: "warn", Format: "json"}, &buf)
	l.Info("hidden")
	l.Warn("shown", slog.String("k", "v"))
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("want 1 line, got %d: %q", len(lines), buf.String())
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &m); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if m["msg"] != "shown" || m["k"] != "v" {
		t.Errorf("unexpected record: %v", m)
	}
}

func TestNewText(t *testing.T) {
	var buf bytes.Buffer
	New(config.Log{Level: "debug", Format: "text"}, &buf).Debug("hello")
	if !strings.Contains(buf.String(), "msg=hello") {
		t.Errorf("text handler expected, got %q", buf.String())
	}
}
