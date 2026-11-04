//go:build loadgen

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coder/websocket"
)

const stgDomain = "staging.zafiro.test"

// target — куда ходит генератор: балансировщик staging (адрес nginx в сети compose, мимо публикации портов, чтобы
// у каждого клуба был свой адрес источника — как у настоящих клубов за NAT; см. srcIP).
type target struct {
	lbIP     string // 172.29.66.10
	webRoots *x509.CertPool
	pkiRoots *x509.CertPool
	srcBase  [4]byte // первый адрес клубов (клуб c — srcBase + c - 1); нули — без привязки
}

func newTarget(stagingDir, lbIP, srcBase string) (*target, error) {
	t := &target{lbIP: lbIP}
	load := func(rel string) (*x509.CertPool, error) {
		b, err := os.ReadFile(filepath.Clean(filepath.Join(stagingDir, rel)))
		if err != nil {
			return nil, err
		}
		p := x509.NewCertPool()
		if !p.AppendCertsFromPEM(b) {
			return nil, fmt.Errorf("%s: нет сертификатов", rel)
		}
		return p, nil
	}
	var err error
	if t.webRoots, err = load("secrets/web/ca.pem"); err != nil {
		return nil, err
	}
	if t.pkiRoots, err = load("secrets/pki/root-ca.pem"); err != nil {
		return nil, err
	}
	if srcBase != "" {
		ip := net.ParseIP(srcBase).To4()
		if ip == nil {
			return nil, fmt.Errorf("-src %q: нужен IPv4", srcBase)
		}
		copy(t.srcBase[:], ip)
	}
	return t, nil
}

// srcIP — адрес источника клуба (nil — любой). Адреса добавляет на мост сети staging tools/loadgen/net.sh.
func (t *target) srcIP(club int) net.IP {
	if t.srcBase == [4]byte{} {
		return nil
	}
	ip := net.IPv4(t.srcBase[0], t.srcBase[1], t.srcBase[2], t.srcBase[3]).To4()
	ip[3] += byte(club - 1) // #nosec G115 -- клубов ≤ 150 (net.sh)
	return ip
}

func (t *target) dialer(club int) *net.Dialer {
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	if ip := t.srcIP(club); ip != nil {
		d.LocalAddr = &net.TCPAddr{IP: ip}
	}
	return d
}

// webClient — HTTPS через nginx (*.staging.zafiro.test → адрес балансировщика) от адреса клуба.
func (t *target) webClient(club int) *http.Client {
	d := t.dialer(club)
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		Proxy:               nil,
		TLSClientConfig:     &tls.Config{RootCAs: t.webRoots, MinVersion: tls.VersionTLS12},
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if host, port, _ := net.SplitHostPort(addr); strings.HasSuffix(host, stgDomain) {
				addr = net.JoinHostPort(t.lbIP, port)
			}
			return d.DialContext(ctx, network, addr)
		},
	}}
}

const appURL = "https://app." + stgDomain

// staffClient — сотрудник клуба: вход, повторный вход по 401 (access-токен живёт 10 мин).
type staffClient struct {
	http  *http.Client
	phone string
	token string
}

type apiError struct {
	Status int
	Body   string
}

func (e *apiError) Error() string { return fmt.Sprintf("HTTP %d: %.200s", e.Status, e.Body) }

func (s *staffClient) login(ctx context.Context) error {
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := s.do(ctx, http.MethodPost, "/api/auth/login", nil, map[string]any{"phone": s.phone, "password": loadPassword}, &out, false); err != nil {
		return err
	}
	if out.AccessToken == "" {
		return errors.New("login: пустой токен")
	}
	s.token = out.AccessToken
	return nil
}

// call — запрос с токеном; 401 — один повторный вход.
func (s *staffClient) call(ctx context.Context, method, path string, hdr map[string]string, body, out any) error {
	err := s.do(ctx, method, path, hdr, body, out, true)
	var ae *apiError
	if errors.As(err, &ae) && ae.Status == http.StatusUnauthorized {
		if err := s.login(ctx); err != nil {
			return err
		}
		return s.do(ctx, method, path, hdr, body, out, true)
	}
	return err
}

func (s *staffClient) do(ctx context.Context, method, path string, hdr map[string]string, body, out any, auth bool) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, appURL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth && s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return &apiError{Status: resp.StatusCode, Body: string(b)}
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

// ws — WebSocket админки через nginx (билет — /api/ws-ticket).
func (s *staffClient) ws(ctx context.Context) (*websocket.Conn, error) {
	var tk struct {
		Ticket string `json:"ticket"`
	}
	if err := s.call(ctx, http.MethodPost, "/api/ws-ticket", nil, nil, &tk); err != nil {
		return nil, err
	}
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(dctx, "wss://app."+stgDomain+"/ws?ticket="+tk.Ticket, &websocket.DialOptions{HTTPClient: s.http})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(4 << 20)
	return c, nil
}
