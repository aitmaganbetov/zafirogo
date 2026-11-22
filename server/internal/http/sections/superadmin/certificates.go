package superadmin

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	superadminapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/superadmin"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/safego"
)

// Сроки HTTPS-сертификатов доменов платформы на экране «Сервер» (аудит CODE-3). Laravel:
// app/Services/Monitoring/Certificates.php, monitoring:certificates (routes/console.php: hourly).
//
// В старом проверку делал планировщик раз в час, экран читал кэш («the page never connects out»). Здесь — проверка в
// памяти инстанса, не чаще раза в час: устаревшие данные экран отдаёт сразу и запускает обновление в фоне (одно
// одновременно), так что экран тоже никогда не ждёт сети. До первой проверки — «Ещё не проверялся», как в старом до
// первого запуска планировщика. Домены — только из конфигурации (не из запроса).
const (
	certRefreshEvery = time.Hour       // ->hourly()
	certDialTimeout  = 5 * time.Second // stream_socket_client(..., 5)
	certRefreshLimit = 30 * time.Second
)

// certificateHosts — Certificates::hosts(): superadmin.ssl_hosts (MONITORING_SSL_HOSTS), иначе хосты адреса сайта
// (http.public_url — APP_URL), кабинета суперадмина (SUPERADMIN_DOMAIN) и реселлера (RESELLER_DOMAIN); без пустых,
// localhost и *.localhost, без повторов. WIKI_DOMAIN в зависимости раздела не передаётся — его домен задаётся в
// superadmin.ssl_hosts.
func certificateHosts(d *httpx.Deps) []string {
	hosts := d.Superadmin.SSLHosts
	if len(hosts) == 0 {
		var public string
		if u, err := url.Parse(d.Superadmin.PublicURL); err == nil {
			public = u.Hostname()
		}
		hosts = []string{public, d.Superadmin.Domain, d.ResellerDomain}
	}
	out := make([]string, 0, len(hosts))
	seen := map[string]bool{}
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if h == "" || h == "localhost" || strings.HasSuffix(h, ".localhost") || seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	return out
}

// certChecker — кэш последней проверки (Cache::forever('monitoring.certificates')) и фоновое обновление.
type certChecker struct {
	// expiresAt — срок сертификата хоста (validTo); ошибка соединения или разбора — ошибка. nil — dialCertificate.
	expiresAt func(ctx context.Context, host string) (time.Time, error)
	log       *slog.Logger

	mu        sync.Mutex
	checkedAt time.Time            // нулевое — ещё не проверялся
	expiry    map[string]time.Time // нет хоста — не удалось проверить
	running   bool
	done      chan struct{} // закрывается по окончании обновления (тесты)
}

// view — строки экрана на момент now; устаревший кэш обновляется в фоне.
func (c *certChecker) view(hosts []string, now time.Time) []superadminapi.CertificateHost {
	c.mu.Lock()
	checkedAt, expiry := c.checkedAt, c.expiry
	if len(hosts) > 0 && !c.running && (checkedAt.IsZero() || now.Sub(checkedAt) >= certRefreshEvery || now.Before(checkedAt)) {
		c.running, c.done = true, make(chan struct{})
		go c.refresh(hosts, c.done)
	}
	c.mu.Unlock()

	out := make([]superadminapi.CertificateHost, 0, len(hosts))
	for _, host := range hosts {
		row := superadminapi.CertificateHost{Host: host, Checked: !checkedAt.IsZero()}
		if at, ok := expiry[host]; ok {
			// (int) floor(($expires - time()) / 86400)
			days := floorDiv(at.Unix()-now.Unix(), 86400)
			row.Days = &days
		}
		out = append(out, row)
	}
	return out
}

// refresh — Certificates::refresh(): все хосты (параллельно, каждый — не дольше certDialTimeout), затем кэш целиком.
func (c *certChecker) refresh(hosts []string, done chan struct{}) {
	defer close(done)
	expiry := map[string]time.Time{}
	err := safego.Call(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), certRefreshLimit)
		defer cancel()
		var (
			wg sync.WaitGroup
			mu sync.Mutex
		)
		for _, host := range hosts {
			wg.Go(func() {
				at, err := c.expiresAt(ctx, host)
				if err != nil {
					c.log.WarnContext(ctx, "superadmin: certificate check", slog.String("host", host), slog.String("err", err.Error()))
					return
				}
				mu.Lock()
				expiry[host] = at
				mu.Unlock()
			})
		}
		wg.Wait()
		return nil
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	c.running = false
	if err != nil {
		c.log.Error("superadmin: certificate check", slog.String("err", err.Error()))
		return
	}
	c.checkedAt, c.expiry = time.Now(), expiry
}

// wait — дождаться текущего обновления (тесты).
func (c *certChecker) wait() {
	c.mu.Lock()
	done := c.done
	c.mu.Unlock()
	if done != nil {
		<-done
	}
}

// dialCertificate — Certificates::expiresAt(): TLS к host:443 с SNI, сертификат не проверяется (verify_peer => false:
// нужен только срок, в том числе у уже истёкшего или чужого сертификата; данные не передаются).
func dialCertificate(ctx context.Context, host string) (time.Time, error) {
	return dialCertificateAddr(ctx, net.JoinHostPort(host, "443"), host)
}

func dialCertificateAddr(ctx context.Context, addr, serverName string) (time.Time, error) {
	d := tls.Dialer{
		NetDialer: &net.Dialer{Timeout: certDialTimeout},
		// Читается только срок сертификата (как verify_peer => false в старом), соединение сразу закрывается.
		Config: &tls.Config{ServerName: serverName, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, // #nosec G402 -- только срок сертификата
	}
	ctx, cancel := context.WithTimeout(ctx, certDialTimeout)
	defer cancel()
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = conn.Close() }()
	tc, ok := conn.(*tls.Conn)
	if !ok {
		return time.Time{}, errors.New("not a TLS connection")
	}
	certs := tc.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return time.Time{}, errors.New("no peer certificate")
	}
	return certs[0].NotAfter, nil
}

// floorDiv — целочисленное деление с округлением вниз (floor() PHP для отрицательных: истёкший сертификат).
func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}
