package enroll

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"strings"
)

// NormalizeMAC — AA:BB:CC:DD:EE:FF или "" для непригодного значения (пусто, все нули, broadcast).
// Laravel: app/Support/MacAddress.php MacAddress::normalize() (BR-127).
func NormalizeMAC(mac string) string {
	var hex []byte
	for i := 0; i < len(mac); i++ {
		c := mac[i]
		switch {
		case c >= '0' && c <= '9', c >= 'A' && c <= 'F':
			hex = append(hex, c)
		case c >= 'a' && c <= 'f':
			hex = append(hex, c-'a'+'A')
		}
	}
	if len(hex) != 12 {
		return ""
	}
	// count(array_unique(str_split($hex))) < 2 — один и тот же символ (000000000000, FFFFFFFFFFFF).
	distinct := false
	for _, c := range hex[1:] {
		if c != hex[0] {
			distinct = true
			break
		}
	}
	if !distinct {
		return ""
	}
	var b strings.Builder
	for i := 0; i < 12; i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.Write(hex[i : i+2])
	}
	return b.String()
}

// MACDeviceID — device_id, который агент сообщает для этого MAC (MAC-AABBCCDDEEFF).
// Laravel: MacAddress::deviceId().
func MACDeviceID(mac string) string { return "MAC-" + strings.ReplaceAll(mac, ":", "") }

// samePC — тот ли это ПК, что на карточке. Laravel: AuthenticateAgentDevice::samePc():
// карточка без device_id и MAC — любой ПК; иначе device совпадает с device_id (без учёта регистра)
// или с MAC-формой MAC карточки. Агент сообщает и UUID, и MAC — достаточно совпадения одного.
func samePC(cardDeviceID, cardMAC *string, hardwareUUID, mac string) bool {
	if cardDeviceID == nil && cardMAC == nil {
		return true
	}
	candidates := []string{strings.TrimSpace(hardwareUUID)}
	if m := NormalizeMAC(mac); m != "" {
		candidates = append(candidates, MACDeviceID(m))
	}
	for _, d := range candidates {
		if d == "" {
			continue
		}
		if cardDeviceID != nil && strings.EqualFold(d, *cardDeviceID) {
			return true
		}
		if cardMAC != nil && strings.EqualFold(d, MACDeviceID(*cardMAC)) {
			return true
		}
	}
	return false
}

// codeAlphabet — 8 символов без похожих: A–Z без I и O, цифры 2–9 (32 символа, enrollment.proto).
const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// CodeLength — длина кода регистрации (40 бит).
const CodeLength = 8

// newCode — случайный код (32 символа алфавита делят 256 без остатка — распределение равномерное).
func newCode() (string, error) {
	buf := make([]byte, CodeLength)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("enroll: code: %w", err)
	}
	for i, b := range buf {
		buf[i] = codeAlphabet[int(b)%len(codeAlphabet)]
	}
	return string(buf), nil
}

// NormalizeCode — код, как его ввёл сотрудник: без пробелов и дефисов, верхний регистр. "" — не код.
func NormalizeCode(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		switch {
		case r == ' ' || r == '-' || r == '\t':
			continue
		case strings.ContainsRune(codeAlphabet, r):
			b.WriteRune(r)
		default:
			return ""
		}
	}
	if b.Len() != CodeLength {
		return ""
	}
	return b.String()
}

// FormatCode — для показа сотруднику: ABCD-EFGH.
func FormatCode(code string) string {
	if len(code) != CodeLength {
		return code
	}
	return code[:4] + "-" + code[4:]
}

// hashCode — HMAC-SHA256(секрет сервера, код). 40 бит кода не выдерживают перебора по утёкшему
// sha256, поэтому хеш — с секретом, который в базе не хранится.
func hashCode(pepper []byte, code string) []byte {
	m := hmac.New(sha256.New, pepper)
	m.Write([]byte(code))
	return m.Sum(nil)
}
