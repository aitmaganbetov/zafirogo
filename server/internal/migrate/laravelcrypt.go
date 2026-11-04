package migrate

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// Расшифровка секретов старого Laravel (решение Q-050): Illuminate\Encryption\Encrypter (laravel/framework v13.35.0,
// config/app.php 'cipher' => 'AES-256-CBC', 'key' => env('APP_KEY'), 'previous_keys' => APP_PREVIOUS_KEYS).
//
//	encrypt(): $iv = random_bytes(16); $value = openssl_encrypt($value, 'aes-256-cbc', $key, 0, $iv) — base64 шифротекста
//	           (PKCS#7); $mac = hash_hmac('sha256', base64($iv).$value, $key);
//	           base64_encode(json_encode(['iv' => base64($iv), 'value' => $value, 'mac' => $mac, 'tag' => ''])).
//	decrypt(): MAC проверяется каждым ключом (текущий, затем previous_keys) через hash_equals; расшифровывает первый
//	           ключ с верным MAC.
//
// Каст 'encrypted' (WebhookEndpoint::$casts['secret']) и Crypt::encryptString (токены Telegram в platform_settings)
// не сериализуют значение (encrypt($value, false)); Crypt::encrypt() — сериализует (serialize()), такие значения
// читаются с unserialize = true (только строки PHP: s:N:"…";).

// LaravelKeys — ключи Encrypter старого Laravel. Значения ключей и расшифрованные секреты не печатаются.
type LaravelKeys struct {
	keys [][]byte
}

var (
	// ErrLaravelKey — APP_KEY не того формата (нужен base64:… на 32 байта или строка из 32 байт, AES-256-CBC).
	ErrLaravelKey = errors.New("migrate: Laravel APP_KEY must be 32 bytes (AES-256-CBC), \"base64:…\" or raw")
	// ErrLaravelPayload — значение не похоже на шифротекст Laravel.
	ErrLaravelPayload = errors.New("migrate: the value is not a Laravel encrypted payload")
	// ErrLaravelMAC — MAC не совпал ни с одним ключом (значение зашифровано другим APP_KEY или испорчено).
	ErrLaravelMAC = errors.New("migrate: the MAC is invalid for every APP_KEY given")
	// ErrLaravelDecrypt — шифротекст не расшифровывается (испорчен).
	ErrLaravelDecrypt = errors.New("migrate: could not decrypt the data")
	// ErrLaravelSerialized — значение не сериализованная строка PHP.
	ErrLaravelSerialized = errors.New("migrate: decrypted value is not a serialized PHP string")
)

// ParseLaravelKeys — APP_KEY и APP_PREVIOUS_KEYS (через запятую, как config/app.php: explode(',', …)).
// Ключ "base64:…" декодируется (Illuminate\Encryption\EncryptionServiceProvider::parseKey()), иначе — байты строки.
func ParseLaravelKeys(appKey, previous string) (*LaravelKeys, error) {
	var out LaravelKeys
	add := func(k string) error {
		k = strings.TrimSpace(k)
		if k == "" {
			return nil
		}
		raw := []byte(k)
		if rest, ok := strings.CutPrefix(k, "base64:"); ok {
			b, err := base64.StdEncoding.DecodeString(rest)
			if err != nil {
				return ErrLaravelKey
			}
			raw = b
		}
		if len(raw) != 32 {
			return ErrLaravelKey
		}
		out.keys = append(out.keys, raw)
		return nil
	}
	if strings.TrimSpace(appKey) == "" {
		return nil, ErrLaravelKey
	}
	if err := add(appKey); err != nil {
		return nil, err
	}
	for k := range strings.SplitSeq(previous, ",") {
		if err := add(k); err != nil {
			return nil, err
		}
	}
	return &out, nil
}

// laravelPayload — JSON внутри base64 (Encrypter::getJsonPayload()).
type laravelPayload struct {
	IV    *string `json:"iv"`
	Value *string `json:"value"`
	MAC   *string `json:"mac"`
	Tag   *string `json:"tag"`
}

// LooksEncrypted — Encrypter::appearsEncrypted(): base64 строгий, JSON с iv, value, mac.
func LooksEncrypted(v string) bool {
	_, err := decodeLaravelPayload(v)
	return err == nil
}

func decodeLaravelPayload(v string) (laravelPayload, error) {
	var p laravelPayload
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v))
	if err != nil {
		return p, ErrLaravelPayload
	}
	if json.Unmarshal(raw, &p) != nil || p.IV == nil || p.Value == nil || p.MAC == nil {
		return p, ErrLaravelPayload
	}
	return p, nil
}

// DecryptString — Crypt::decryptString() / каст 'encrypted' (без unserialize).
func (k *LaravelKeys) DecryptString(payload string) (string, error) { return k.Decrypt(payload, false) }

// Decrypt — Encrypter::decrypt($payload, $unserialize).
func (k *LaravelKeys) Decrypt(payload string, unserialize bool) (string, error) {
	if k == nil || len(k.keys) == 0 {
		return "", ErrLaravelKey
	}
	p, err := decodeLaravelPayload(payload)
	if err != nil {
		return "", err
	}
	iv, err := base64.StdEncoding.Strict().DecodeString(*p.IV)
	if err != nil || len(iv) != aes.BlockSize {
		return "", ErrLaravelPayload // validPayload(): strlen(base64_decode(iv, true)) === 16
	}
	if p.Tag != nil && *p.Tag != "" {
		return "", ErrLaravelPayload // ensureTagIsValid(): тег у CBC — ошибка
	}
	var key []byte
	for _, candidate := range k.keys {
		mac := hmac.New(sha256.New, candidate)
		mac.Write([]byte(*p.IV + *p.Value))
		if hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(*p.MAC)) {
			key = candidate
			break
		}
	}
	if key == nil {
		return "", ErrLaravelMAC
	}
	ct, err := base64.StdEncoding.DecodeString(*p.Value) // openssl_decrypt(..., 0, …): base64-шифротекст
	if err != nil || len(ct) == 0 || len(ct)%aes.BlockSize != 0 {
		return "", ErrLaravelDecrypt
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", ErrLaravelDecrypt
	}
	plain := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, ct)
	n := int(plain[len(plain)-1])
	if n == 0 || n > aes.BlockSize || n > len(plain) {
		return "", ErrLaravelDecrypt
	}
	for _, b := range plain[len(plain)-n:] {
		if int(b) != n {
			return "", ErrLaravelDecrypt
		}
	}
	out := string(plain[:len(plain)-n])
	if unserialize {
		return phpUnserializeString(out)
	}
	return out, nil
}

// phpUnserializeString — unserialize() для строки PHP: s:<байт>:"<байты>";.
func phpUnserializeString(s string) (string, error) {
	rest, ok := strings.CutPrefix(s, "s:")
	if !ok {
		return "", ErrLaravelSerialized
	}
	lenStr, rest, ok := strings.Cut(rest, ":")
	if !ok {
		return "", ErrLaravelSerialized
	}
	n, err := strconv.Atoi(lenStr)
	if err != nil || n < 0 || len(rest) != n+3 || rest[0] != '"' || rest[n+1:] != `";` {
		return "", ErrLaravelSerialized
	}
	return rest[1 : n+1], nil
}
