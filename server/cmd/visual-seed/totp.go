package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/mfa"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/totp"
	"github.com/aitmaganbetov/zafirogo/server/internal/secretbox"
)

// secondFactor — у суперадминов демо-данных TOTP уже включён известным ключом login.totp_secret (Этап 7): вход снимков
// вводит код (web/e2e/visual/support.ts), а не проходит настройку, которая писала бы в журнал платформы лишнюю
// строку по сравнению со старым. Ключ шифруется ключом данных VISUAL_DATA_KEY ("kid:секрет", tools/visual/new.sh
// передаёт его же серверу в ZAFIRO_SECURITY_DATA_KEYS). Без ключа или секрета шаг пропускается.
func (s *seeder) secondFactor(ctx context.Context) error {
	raw := os.Getenv("VISUAL_DATA_KEY")
	if s.f.Login.TOTPSecret == "" || raw == "" {
		fmt.Fprintln(os.Stderr, "visual-seed: VISUAL_DATA_KEY или login.totp_secret не заданы — TOTP суперадмина не настроен")
		return nil
	}
	if _, err := totp.DecodeSecret(s.f.Login.TOTPSecret); err != nil {
		return fmt.Errorf("login.totp_secret: %w", err)
	}
	kid, secret, ok := strings.Cut(raw, ":")
	if !ok {
		return fmt.Errorf("VISUAL_DATA_KEY: ожидается kid:секрет")
	}
	box, err := secretbox.NewKeyring([]secretbox.Key{{ID: kid, Secret: secret}}, "")
	if err != nil {
		return err
	}
	for _, u := range s.f.Users {
		if !u.IsSuperadmin {
			continue
		}
		sealed, err := box.SealWith(s.f.Login.TOTPSecret, mfa.SecretContext(u.ID))
		if err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO superadmin_totp (user_id, secret, confirmed_at, last_step, created_at, updated_at)
			VALUES ($1, $2, $3, 0, $3, $3)`, u.ID, sealed, s.now); err != nil {
			return err
		}
	}
	return nil
}
