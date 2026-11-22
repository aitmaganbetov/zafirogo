package db

import (
	"runtime"
	"testing"
)

// Размер пула по умолчанию — max(16, 4 × GOMAXPROCS), а не умолчание pgx max(4, CPU) (нагрузочный тест O4,
// docs/load-test.md §4); явный pool_max_conns в DSN — как задан.
func TestParsePoolConfigMaxConns(t *testing.T) {
	want := int32(min(max(16, 4*runtime.GOMAXPROCS(0)), 256))
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

// Аудит OPS-8: размер пула следует GOMAXPROCS (квота CPU контейнера), а не числу ядер узла.
func TestDefaultMaxConnsFollowsGOMAXPROCS(t *testing.T) {
	prev := runtime.GOMAXPROCS(0)
	t.Cleanup(func() { runtime.GOMAXPROCS(prev) })
	for _, tc := range []struct {
		procs int
		want  int32
	}{{1, 16}, {2, 16}, {8, 32}, {33, 132}, {100, 256}} {
		runtime.GOMAXPROCS(tc.procs)
		if got := DefaultMaxConns(); got != tc.want {
			t.Errorf("GOMAXPROCS=%d (NumCPU=%d): DefaultMaxConns = %d, want %d", tc.procs, runtime.NumCPU(), got, tc.want)
		}
	}
}
