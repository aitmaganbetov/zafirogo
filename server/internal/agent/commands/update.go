package commands

// Обновление агента (UpdateAvailable). Решение «Обновления агента» (docs/DECISIONS.md): подпись Ed25519
// обязательна; ключ подписи хранится в CI-секретах, открытый ключ вшит в агента. Сервер не подписывает —
// он только проверяет подпись CI открытыми ключами из конфигурации (agent.update.public_keys) перед рассылкой,
// чтобы неподписанное или испорченное обновление не ушло на ПК клубов (агент проверит ещё раз своим ключом).

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
)

// UpdateKeys — открытые ключи Ed25519 подписи обновлений по key_id.
type UpdateKeys map[string]ed25519.PublicKey

// UpdateSigningMessage — строка, которую подписывает CI (agent.proto, UpdateAvailable):
// "zafiro-agent-update/v1\n" + version + "\n" + sha256 + "\n" + size_bytes + "\n" + channel (UTF-8).
func UpdateSigningMessage(u *agentv1.UpdateAvailable) []byte {
	return []byte("zafiro-agent-update/v1\n" + u.GetVersion() + "\n" + u.GetSha256() + "\n" +
		strconv.FormatInt(u.GetSizeBytes(), 10) + "\n" + u.GetChannel())
}

// validateUpdate — обязательные поля UpdateAvailable, включая подпись (без проверки самой подписи).
func validateUpdate(u *agentv1.UpdateAvailable) error {
	if u == nil {
		return fmt.Errorf("%w: update_available is empty", ErrInvalidCommand)
	}
	if u.GetVersion() == "" || len(u.GetVersion()) > 64 || strings.ContainsAny(u.GetVersion(), "\n\r") {
		return fmt.Errorf("%w: update_available.version is required", ErrInvalidCommand)
	}
	if u.GetChannel() == "" || len(u.GetChannel()) > 32 || strings.ContainsAny(u.GetChannel(), "\n\r") {
		return fmt.Errorf("%w: update_available.channel is required", ErrInvalidCommand)
	}
	if !isLowerHex(u.GetSha256(), 64) {
		return fmt.Errorf("%w: update_available.sha256 must be 64 lowercase hex characters", ErrInvalidCommand)
	}
	if u.GetSizeBytes() <= 0 {
		return fmt.Errorf("%w: update_available.size_bytes must be > 0", ErrInvalidCommand)
	}
	if ur, err := url.Parse(u.GetUrl()); err != nil || ur.Scheme != "https" || ur.Host == "" || ur.User != nil {
		return fmt.Errorf("%w: update_available.url must be an https URL", ErrInvalidCommand)
	}
	if len(u.GetSignatureEd25519()) != ed25519.SignatureSize {
		return fmt.Errorf("%w: update_available.signature_ed25519 is required (%d bytes)", ErrInvalidCommand, ed25519.SignatureSize)
	}
	if u.GetKeyId() == "" {
		return fmt.Errorf("%w: update_available.key_id is required", ErrInvalidCommand)
	}
	return nil
}

// VerifyUpdate проверяет поля и подпись обновления ключом key_id. Нет ключей — отказ (без проверки
// подписи обновление не рассылается).
func VerifyUpdate(u *agentv1.UpdateAvailable, keys UpdateKeys) error {
	if err := validateUpdate(u); err != nil {
		return err
	}
	pub, ok := keys[u.GetKeyId()]
	if !ok || len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: update_available.key_id %q is not a configured signing key", ErrInvalidCommand, u.GetKeyId())
	}
	if !ed25519.Verify(pub, UpdateSigningMessage(u), u.GetSignatureEd25519()) {
		return fmt.Errorf("%w: update_available.signature_ed25519 does not verify", ErrInvalidCommand)
	}
	return nil
}

func isLowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	if _, err := hex.DecodeString(s); err != nil {
		return false
	}
	return strings.ToLower(s) == s
}
