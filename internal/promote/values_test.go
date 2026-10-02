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

func TestPatchRuntimeRewritesRuntimeFields(t *testing.T) {
	in := []byte("image: keep\nport: 8080\nhealthcheck: /\nhostname: old.example.org\nnetworkPolicy:\n  enabled: true\n  internet: true\n  peers: []\n")
	off := false
	out, err := PatchRuntime(in, RuntimePatch{
		Hostname:    "new.example.org",
		Port:        9000,
		Healthcheck: "healthz",
		Internet:    &off,
		Peers:       []string{"billing", "postgres:web"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, "image: keep") {
		t.Fatalf("image lost:\n%s", got)
	}
	if !strings.Contains(got, "port: 9000") {
		t.Fatalf("port not patched:\n%s", got)
	}
	if !strings.Contains(got, "healthcheck: /healthz") {
		t.Fatalf("healthcheck not patched:\n%s", got)
	}
	if !strings.Contains(got, "hostname: new.example.org") {
		t.Fatalf("hostname not patched:\n%s", got)
	}
	if !strings.Contains(got, "internet: false") {
		t.Fatalf("internet:\n%s", got)
	}
	if !strings.Contains(got, "billing") || !strings.Contains(got, "postgres:web") {
		t.Fatalf("peers:\n%s", got)
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
