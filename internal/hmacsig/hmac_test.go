package hmacsig

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"
)

func TestValid(t *testing.T) {
	body := []byte(`{"ref":"refs/heads/main"}`)
	secret := "s3cret"
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))
	if !Valid(secret, sig, body) {
		t.Fatal("expected valid")
	}
	if !Valid(secret, "sha256="+sig, body) {
		t.Fatal("expected valid with prefix")
	}
	if Valid(secret, sig, append(body, 'x')) {
		t.Fatal("expected invalid for mutated body")
	}
	if Valid("other", sig, body) {
		t.Fatal("expected invalid for other secret")
	}
}

func TestHeaderAndDelivery(t *testing.T) {
	h := make(http.Header)
	h.Set("X-Gitea-Signature", "abc")
	h.Set("X-Gitea-Delivery", "del-1")
	h.Set("X-Gitea-Event", "push")
	if Header(h) != "abc" {
		t.Fatalf("header %q", Header(h))
	}
	if DeliveryID(h) != "del-1" {
		t.Fatalf("delivery %q", DeliveryID(h))
	}
	if Event(h) != "push" {
		t.Fatalf("event %q", Event(h))
	}
}

func TestNewSecret(t *testing.T) {
	a, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if a == b || len(a) != 64 {
		t.Fatalf("got %q %q", a, b)
	}
}
