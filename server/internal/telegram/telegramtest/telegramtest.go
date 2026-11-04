// Package telegramtest — httptest-сервер, повторяющий ответы Telegram Gateway и Bot API (успех и ошибки) для
// тестов: сеть наружу не нужна. Gateway — по адресу GatewayURL(), бот — BotURL().
package telegramtest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Request — принятый запрос.
type Request struct {
	API    string // "gateway" или "bot"
	Method string // метод API (sendVerificationMessage, sendMessage…)
	Auth   string // заголовок Authorization (Gateway)
	Token  string // токен бота из адреса
	Body   map[string]any
}

// Reply — ответ на запрос: код HTTP и JSON-тело (nil — тело "not json", как сбой прокси).
type Reply struct {
	Status int
	Body   any
}

// Server — поддельный Telegram.
type Server struct {
	*httptest.Server

	mu       sync.Mutex
	requests []Request
	// Gateway и Bot — ответы по методу; nil — ответы по умолчанию (OK).
	gateway func(r Request) Reply
	bot     func(r Request) Reply
}

// New — сервер с ответами по умолчанию: Gateway отправляет коды (ok, request_id), проверка статуса неизвестного
// request_id — REQUEST_ID_INVALID (токен верный); бот — getMe ok, getUpdates пусто, sendMessage ok.
func New(t testing.TB) *Server {
	s := &Server{}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// GatewayURL — адрес для telegram.GatewayClient.URL.
func (s *Server) GatewayURL() string { return s.URL + "/gateway/" }

// BotURL — адрес для telegram.BotClient.URL.
func (s *Server) BotURL() string { return s.URL + "/botapi/" }

// OnGateway задаёт ответы Gateway.
func (s *Server) OnGateway(fn func(r Request) Reply) { s.mu.Lock(); s.gateway = fn; s.mu.Unlock() }

// OnBot задаёт ответы бота.
func (s *Server) OnBot(fn func(r Request) Reply) { s.mu.Lock(); s.bot = fn; s.mu.Unlock() }

// Requests — все принятые запросы.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Codes — коды, которые Gateway просили отправить (sendVerificationMessage), по порядку.
func (s *Server) Codes() []string {
	var out []string
	for _, r := range s.Requests() {
		if r.API == "gateway" && r.Method == "sendVerificationMessage" {
			c, _ := r.Body["code"].(string)
			out = append(out, c)
		}
	}
	return out
}

// Messages — тексты sendMessage бота, по порядку.
func (s *Server) Messages() []string {
	var out []string
	for _, r := range s.Requests() {
		if r.API == "bot" && r.Method == "sendMessage" {
			t, _ := r.Body["text"].(string)
			out = append(out, t)
		}
	}
	return out
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	req := Request{Auth: r.Header.Get("Authorization"), Body: map[string]any{}}
	_ = json.Unmarshal(raw, &req.Body)
	path := r.URL.Path
	switch {
	case strings.HasPrefix(path, "/gateway/"):
		req.API, req.Method = "gateway", strings.TrimPrefix(path, "/gateway/")
	case strings.HasPrefix(path, "/botapi/bot"):
		rest := strings.TrimPrefix(path, "/botapi/bot")
		i := strings.LastIndex(rest, "/")
		if i < 0 {
			http.NotFound(w, r)
			return
		}
		req.API, req.Token, req.Method = "bot", rest[:i], rest[i+1:]
	default:
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	s.requests = append(s.requests, req)
	fn := s.gateway
	if req.API == "bot" {
		fn = s.bot
	}
	s.mu.Unlock()
	var rep Reply
	if fn != nil {
		rep = fn(req)
	} else {
		rep = defaultReply(req)
	}
	if rep.Status == 0 {
		rep.Status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(rep.Status)
	if rep.Body == nil {
		_, _ = w.Write([]byte("not json"))
		return
	}
	_ = json.NewEncoder(w).Encode(rep.Body)
}

func defaultReply(r Request) Reply {
	switch {
	case r.API == "gateway" && r.Method == "checkVerificationStatus":
		return Reply{Status: 400, Body: map[string]any{"ok": false, "error": "REQUEST_ID_INVALID"}}
	case r.API == "gateway":
		return Reply{Body: map[string]any{"ok": true, "result": map[string]any{"request_id": "r1"}}}
	case r.Method == "getUpdates":
		return Reply{Body: map[string]any{"ok": true, "result": []any{}}}
	default:
		return Reply{Body: map[string]any{"ok": true, "result": map[string]any{"id": 1}}}
	}
}

// GatewayFails — ответ Gateway «не отправлено» (PHONE_NUMBER_INVALID), как fakeGateway(sends: false).
func GatewayFails(r Request) Reply {
	if r.Method == "sendVerificationMessage" {
		return Reply{Status: 400, Body: map[string]any{"ok": false, "error": "PHONE_NUMBER_INVALID"}}
	}
	return defaultReply(r)
}
