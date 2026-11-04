//go:build loadgen

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/password"
	"github.com/aitmaganbetov/zafirogo/server/internal/pki"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// pcCred — ПК нагрузочного набора с сертификатом (файл -certs).
type pcCred struct {
	Club       int    `json:"club"`
	PC         int    `json:"pc"`
	ComputerID int64  `json:"computer_id"`
	ZoneID     int64  `json:"zone_id"`
	CertPEM    string `json:"cert"`
	KeyPEM     string `json:"key"`
}

func cmdPrepare(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("prepare", flag.ExitOnError)
	dir := fs.String("staging", "../deploy/staging", "каталог deploy/staging")
	certsOut := fs.String("certs", "", "куда записать сертификаты и ключи ПК (JSON, только локально)")
	webhookClubs := fs.Int("webhook-clubs", 10, "клубов с вебхуком (http://legacy/hook/<club>; нужен tools/loadgen/compose.load.yaml)")
	enrollSample := fs.Int("enroll-sample", 50, "ПК, регистрируемых настоящим путём (код через REST → Enroll через nginx) — замер регистрации")
	lbIP := fs.String("lb", "172.29.66.10", "адрес nginx в сети staging")
	src := fs.String("src", "", "первый адрес источника клубов (tools/loadgen/net.sh), пусто — без привязки")
	_ = fs.Parse(args)
	if *certsOut == "" {
		return errors.New("prepare: -certs обязателен")
	}
	env, err := loadStaging(*dir)
	if err != nil {
		return err
	}
	pool, err := pgxpool.New(ctx, env.ownerDSN())
	if err != nil {
		return err
	}
	defer pool.Close()

	// 1. Пароли: Argon2id вместо bcrypt (иначе первый вход каждого гостя — bcrypt cost 12 + перехеширование).
	h, err := password.New(password.Options{})
	if err != nil {
		return err
	}
	hash, err := h.Hash(ctx, loadPassword)
	if err != nil {
		return err
	}
	tag, err := pool.Exec(ctx, `UPDATE users SET password = $1 WHERE id >= 10000`, hash)
	if err != nil {
		return err
	}
	fmt.Printf("prepare: пароли %d пользователей → Argon2id\n", tag.RowsAffected())

	// 2. Сертификаты ПК — прямой выпуск от staging-CA (как Enroll: pki.CA.Issue + строка agent_certificates).
	// Лимиты регистрации в коде не трогаются: настоящий путь меряется отдельно (enroll-sample).
	ca, err := pki.LoadCAFiles(filepath.Join(*dir, "secrets/pki/agent-ca.pem"), filepath.Join(*dir, "secrets/pki/agent-ca.key"))
	if err != nil {
		return fmt.Errorf("staging CA: %w", err)
	}
	type row struct {
		club, id, zone int64
	}
	rows, err := pool.Query(ctx, `SELECT club_id, id, club_zone_id FROM club_computers WHERE id = club_id * 10000 + number AND id >= 10000 ORDER BY id`)
	if err != nil {
		return err
	}
	pcs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		var zone *int64
		err := r.Scan(&x.club, &x.id, &zone)
		if zone != nil {
			x.zone = *zone
		}
		return x, err
	})
	if err != nil {
		return err
	}
	if len(pcs) == 0 {
		return errors.New("prepare: нет ПК нагрузочного набора — сначала fixture + visual-seed")
	}
	start := time.Now()
	creds := make([]pcCred, len(pcs))
	issued := make([]pki.Issued, len(pcs))
	var wg sync.WaitGroup
	errc := make(chan error, 1)
	sem := make(chan struct{}, 8)
	for i, p := range pcs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err == nil {
				var der []byte
				der, err = x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
				if err == nil {
					var csr *x509.CertificateRequest
					if csr, err = x509.ParseCertificateRequest(der); err == nil {
						issued[i], err = ca.Issue(pki.IssueRequest{CSR: csr, ClubID: p.club, ComputerID: p.id})
					}
				}
			}
			if err == nil {
				var kd []byte
				if kd, err = x509.MarshalPKCS8PrivateKey(key); err == nil {
					creds[i] = pcCred{Club: int(p.club), PC: int(p.id % 10000), ComputerID: p.id, ZoneID: p.zone, CertPEM: issued[i].CertPEM,
						KeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd}))}
				}
			}
			if err != nil {
				select {
				case errc <- err:
				default:
				}
			}
		}()
	}
	wg.Wait()
	select {
	case err := <-errc:
		return err
	default:
	}
	byClub := map[int64][]int{}
	for i, p := range pcs {
		byClub[p.club] = append(byClub[p.club], i)
	}
	for club, idx := range byClub {
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, "SELECT set_config('app.club_id', $1, true)", strconv.FormatInt(club, 10)); err != nil {
				return err
			}
			// Повторный prepare: прежние сертификаты набора отзываются (как повторный Enroll).
			if _, err := tx.Exec(ctx, `UPDATE agent_certificates SET revoked_at = now(), revoke_reason = 'reenrolled', changed_at = now()
				WHERE club_id = $1 AND revoked_at IS NULL`, club); err != nil {
				return err
			}
			q := store.New(tx)
			for _, i := range idx {
				is := issued[i]
				if err := q.InsertAgentCertificate(ctx, store.InsertAgentCertificateParams{Serial: is.Serial, ClubID: club,
					ClubComputerID: pcs[i].id, SpkiSha256: is.SPKISHA256, NotBefore: is.NotBefore, NotAfter: is.NotAfter, IssuedVia: "enroll"}); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("сертификаты клуба %d: %w", club, err)
		}
	}
	b, err := json.Marshal(creds)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*certsOut, b, 0o600); err != nil {
		return err
	}
	fmt.Printf("prepare: %d сертификатов ПК за %s → %s\n", len(creds), time.Since(start).Round(time.Millisecond), *certsOut)

	t, err := newTarget(*dir, *lbIP, *src)
	if err != nil {
		return err
	}
	// 3. Вебхуки (outbox → доставка): http://legacy/hook/<club>, все события.
	for c := 1; c <= *webhookClubs && c <= len(byClub); c++ {
		s := &staffClient{http: t.webClient(c), phone: phone(c, ownerK)}
		if err := s.login(ctx); err != nil {
			return fmt.Errorf("вход владельца клуба %d: %w", c, err)
		}
		err := s.call(ctx, http.MethodPost, "/api/settings/integrations/webhooks", nil, map[string]any{
			"url": fmt.Sprintf("http://legacy/hook/%d", c), "events": []string{"product.created", "product.updated", "stock.changed",
				"sale.completed", "computer.reservation_changed", "computer.controlled"}}, nil)
		if err != nil {
			return fmt.Errorf("вебхук клуба %d: %w", c, err)
		}
	}
	fmt.Printf("prepare: вебхуки в %d клубах\n", min(*webhookClubs, len(byClub)))

	// 4. Настоящая регистрация (выборка): код через REST владельцем клуба 1 → Enroll через nginx (L4, PROXY protocol).
	if *enrollSample > 0 {
		if err := enrollSampleRun(ctx, t, *enrollSample); err != nil {
			return fmt.Errorf("enroll-sample: %w", err)
		}
	}
	return nil
}

// enrollSampleRun — K регистраций по одноразовым кодам (зона «Не назначена»); печатает p50/p95 выдачи кода и Enroll. Зарегистрированные ПК
// отзываются (POST …/revoke), чтобы не мешать основному прогону.
func enrollSampleRun(ctx context.Context, t *target, k int) error {
	const club = 1
	s := &staffClient{http: t.webClient(club), phone: phone(club, ownerK)}
	if err := s.login(ctx); err != nil {
		return err
	}
	var codeLat, enrollLat []time.Duration
	var ids []int64
	conn, err := grpc.NewClient("passthrough:///"+t.lbIP+":8443",
		grpc.WithTransportCredentials(credentials.NewTLS(agentTLS(t, nil))), grpc.WithNoProxy(),
		grpc.WithContextDialer(dialFrom(t, club)))
	if err != nil {
		return err
	}
	defer conn.Close()
	cl := agentv1.NewEnrollmentServiceClient(conn)
	for i := 0; i < k; i++ {
		t0 := time.Now()
		var code struct {
			Code string `json:"code"`
		}
		if err := s.call(ctx, http.MethodPost, "/api/agent-enrollment/codes", nil, map[string]any{}, &code); err != nil {
			return err
		}
		codeLat = append(codeLat, time.Since(t0))
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return err
		}
		der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
		if err != nil {
			return err
		}
		t1 := time.Now()
		resp, err := cl.Enroll(ctx, &agentv1.EnrollRequest{EnrollmentCode: code.Code,
			CsrPem: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})),
			Device: &agentv1.DeviceInfo{HardwareUuid: fmt.Sprintf("LOAD-ENROLL-%d-%d", time.Now().UnixNano(), i),
				MacAddress: fmt.Sprintf("02:EE:00:%02X:%02X:%02X", (time.Now().Unix()>>8)&0xff, time.Now().Unix()&0xff, i&0xff),
				Hostname:   fmt.Sprintf("ENROLL-%02d", i), AgentVersion: "2.0.0", OsVersion: "Windows 11 Pro"}})
		if err != nil {
			return fmt.Errorf("enroll #%d: %w", i, err)
		}
		enrollLat = append(enrollLat, time.Since(t1))
		ids = append(ids, resp.GetComputerId())
	}
	for _, id := range ids {
		if err := s.call(ctx, http.MethodPost, fmt.Sprintf("/api/agent-enrollment/computers/%d/revoke", id), nil, nil, nil); err != nil {
			fmt.Printf("prepare: revoke %d: %v\n", id, err)
		}
	}
	fmt.Printf("prepare: регистрация %d ПК по кодам: код (REST) p50 %s p95 %s; Enroll (gRPC через nginx) p50 %s p95 %s\n", k,
		pct(codeLat, 50), pct(codeLat, 95), pct(enrollLat, 50), pct(enrollLat, 95))
	return nil
}

func pct(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i := int(float64(len(s)-1) * p / 100)
	return s[i].Round(100 * time.Microsecond)
}

func readCreds(path string) ([]pcCred, error) {
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	var c []pcCred
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	sort.Slice(c, func(i, j int) bool { return c[i].ComputerID < c[j].ComputerID })
	return c, nil
}
