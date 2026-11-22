package db

import (
	"context"
	"net"
	"syscall"
	"testing"
	"time"
)

func sockopt(t *testing.T, c net.Conn, level, opt int) int {
	t.Helper()
	raw, err := c.(*net.TCPConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var v int
	var gerr error
	if err := raw.Control(func(fd uintptr) { v, gerr = syscall.GetsockoptInt(int(fd), level, opt) }); err != nil { // #nosec G115
		t.Fatal(err)
	}
	if gerr != nil {
		t.Fatal(gerr)
	}
	return v
}

// Аудит OPS-3 (клиент): соединения пула — с keepalive 30 с / 10 с × 3 и TCP_USER_TIMEOUT 60 с; connect_timeout из DSN
// сохраняется.
func TestPoolDialKeepalive(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		accepted <- c
	}()
	cfg, err := ParsePoolConfig("postgres://u:p@" + ln.Addr().String() + "/zafiro?sslmode=disable&connect_timeout=7")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.ConnectTimeout != 7*time.Second {
		t.Fatalf("ConnectTimeout = %v", cfg.ConnConfig.ConnectTimeout)
	}
	c, err := cfg.ConnConfig.DialFunc(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if sc := <-accepted; sc != nil {
		defer func() { _ = sc.Close() }()
	}
	for name, tc := range map[string]struct{ level, opt, want int }{
		"SO_KEEPALIVE":     {syscall.SOL_SOCKET, syscall.SO_KEEPALIVE, 1},
		"TCP_KEEPIDLE":     {syscall.IPPROTO_TCP, syscall.TCP_KEEPIDLE, 30},
		"TCP_KEEPINTVL":    {syscall.IPPROTO_TCP, syscall.TCP_KEEPINTVL, 10},
		"TCP_KEEPCNT":      {syscall.IPPROTO_TCP, syscall.TCP_KEEPCNT, 3},
		"TCP_USER_TIMEOUT": {syscall.IPPROTO_TCP, tcpUserTimeout, 60000},
	} {
		if got := sockopt(t, c, tc.level, tc.opt); got != tc.want {
			t.Errorf("%s = %d, want %d", name, got, tc.want)
		}
	}
}
