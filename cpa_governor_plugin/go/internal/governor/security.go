package governor

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

func SHA256Hex(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:])
}

func NormalizeHash(value string) (string, error) {
	text := strings.TrimSpace(value)
	text = strings.TrimPrefix(text, "sha256:")
	if len(text) != 64 {
		return "", errors.New("invalid hash length")
	}
	_, err := hex.DecodeString(text)
	if err != nil {
		return "", err
	}
	return strings.ToLower(text), nil
}

func HashPreview(value string) string {
	normalized, err := NormalizeHash(value)
	if err != nil {
		normalized = SHA256Hex(value)
	}
	if len(normalized) <= 16 {
		return normalized
	}
	return normalized[:8] + "..." + normalized[len(normalized)-6:]
}

type SessionPayload struct {
	KeyID     string `json:"key_id"`
	KeyHash   string `json:"key_hash"`
	ExpiresAt int64  `json:"expires_at"`
}

func SignSession(payload SessionPayload, secret string) (string, error) {
	if strings.TrimSpace(secret) == "" {
		return "", errors.New("session secret is required")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(body))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return body + "." + sig, nil
}

func VerifySession(token, secret string, now time.Time) (SessionPayload, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || strings.TrimSpace(secret) == "" {
		return SessionPayload{}, false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(parts[0]))
	want := mac.Sum(nil)
	got, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(got, want) {
		return SessionPayload{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return SessionPayload{}, false
	}
	var payload SessionPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return SessionPayload{}, false
	}
	if payload.ExpiresAt > 0 && now.Unix() > payload.ExpiresAt {
		return SessionPayload{}, false
	}
	return payload, true
}
