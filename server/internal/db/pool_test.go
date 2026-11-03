package db

import (
	"runtime"
	"testing"
)

// Размер пула по умолчанию — max(16, 4 × CPU), а не умолчание pgx max(4, CPU) (нагрузочный тест O4,
// docs/load-test.md §4); явный pool_max_conns в DSN — как задан.
func TestParsePoolConfigMaxConns(t *testing.T) {
	want := int32(max(16, 4*runtime.NumCPU()))
	for _, tc := range []struct {
		name, dsn string
		want      int32
	}{
		{"url default", "postgres://u:p@db:5432/zafiro?sslmode=disable", want},
		{"kv default", "host=db user=u dbname=zafiro sslmode=disable", want},
		{"url explicit", "postgres://u:p@db:5432/zafiro?sslmode=disable&pool_max_conns=7", 7},
		{"kv explicit", "host=db user=u dbname=zafiro pool_max_conns=3", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParsePoolConfig(tc.dsn)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.MaxConns != tc.want {
				t.Fatalf("MaxConns = %d, want %d", cfg.MaxConns, tc.want)
			}
		})
	}
	if DefaultMaxConns() < 16 {
		t.Fatalf("DefaultMaxConns = %d", DefaultMaxConns())
	}
}
