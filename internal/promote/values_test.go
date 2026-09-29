package promote

import (
	"strings"
	"testing"
)

func TestPatchImageReplacesOnlyImage(t *testing.T) {
	in := []byte("hostname: web.example.org\nimage: old\nport: 8080\nhealthcheck: /healthz\n")
	out, err := PatchImage(in, "git.host.bzh/pepe/web:sha-abc@sha256:def")
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, "hostname: web.example.org") {
		t.Fatalf("hostname lost:\n%s", got)
	}
	if !strings.Contains(got, "port: 8080") {
		t.Fatalf("port lost:\n%s", got)
	}
	if !strings.Contains(got, "healthcheck: /healthz") {
		t.Fatalf("healthcheck lost:\n%s", got)
	}
	if !strings.Contains(got, `image: "git.host.bzh/pepe/web:sha-abc@sha256:def"`) &&
		!strings.Contains(got, "image: git.host.bzh/pepe/web:sha-abc@sha256:def") {
		t.Fatalf("image not patched:\n%s", got)
	}
	if strings.Contains(got, "image: old") {
		t.Fatalf("old image still present:\n%s", got)
	}
}

func TestPatchImageAddsMissingKey(t *testing.T) {
	out, err := PatchImage([]byte("port: 8080\n"), "repo:sha-abc@sha256:def")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "port: 8080") {
		t.Fatalf("port lost:\n%s", out)
	}
	if !strings.Contains(string(out), "image:") {
		t.Fatalf("image not added:\n%s", out)
	}
}

func TestPatchImageRejectsEmptyAndNonMapping(t *testing.T) {
	if _, err := PatchImage(nil, "x"); err == nil {
		t.Fatal("expected error for empty")
	}
	if _, err := PatchImage([]byte("- item\n"), "x"); err == nil {
		t.Fatal("expected error for sequence")
	}
}
