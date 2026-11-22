package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCheck(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		if status == 0 {
			time.Sleep(300 * time.Millisecond)
			return
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()

	if err := check(context.Background(), srv.URL+"/healthz", time.Second); err != nil {
		t.Fatalf("200: %v", err)
	}
	status = http.StatusServiceUnavailable
	if err := check(context.Background(), srv.URL+"/healthz", time.Second); err == nil {
		t.Fatal("503 должен быть ошибкой")
	}
	status = 0 // зависший процесс
	if err := check(context.Background(), srv.URL+"/healthz", 50*time.Millisecond); err == nil {
		t.Fatal("таймаут должен быть ошибкой")
	}
	srv.Close()
	if err := check(context.Background(), srv.URL+"/healthz", time.Second); err == nil {
		t.Fatal("нет процесса — ошибка")
	}
}
