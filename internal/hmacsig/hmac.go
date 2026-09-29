package hmacsig

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
)

// NewSecret returns a 32-byte hex secret. It is never stored in SQLite.
func NewSecret() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Header returns the first Forgejo/Gitea/GitHub signature header.
func Header(h http.Header) string {
	for _, key := range []string{"X-Forgejo-Signature", "X-Gitea-Signature", "X-Hub-Signature-256"} {
		if v := strings.TrimSpace(h.Get(key)); v != "" {
			return v
		}
	}
	return ""
}

// Valid reports whether sig is HMAC-SHA256 hex of body with secret.
func Valid(secret, sig string, body []byte) bool {
	secret = strings.TrimSpace(secret)
	sig = strings.TrimSpace(sig)
	if secret == "" || sig == "" {
		return false
	}
	sig = strings.TrimPrefix(strings.ToLower(sig), "sha256=")
	want, err := hex.DecodeString(sig)
	if err != nil || len(want) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	sum := mac.Sum(nil)
	return subtle.ConstantTimeCompare(sum, want) == 1
}

func Hex(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func DeliveryID(h http.Header) string {
	for _, key := range []string{"X-Forgejo-Delivery", "X-Gitea-Delivery", "X-GitHub-Delivery", "X-Gitea-Delivery-Id"} {
		if v := strings.TrimSpace(h.Get(key)); v != "" {
			return v
		}
	}
	return ""
}

func Event(h http.Header) string {
	for _, key := range []string{"X-Forgejo-Event", "X-Gitea-Event", "X-GitHub-Event"} {
		if v := strings.TrimSpace(h.Get(key)); v != "" {
			return strings.ToLower(v)
		}
	}
	return ""
}
