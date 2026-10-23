package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorRequiresAnExplicitContextAndConfirmation(t *testing.T) {
	for _, test := range []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "sous-commande absente", args: nil, wantErr: "doctor network"},
		{name: "sous-commande inconnue", args: []string{"cluster"}, wantErr: "doctor network"},
		{name: "contexte implicite", args: []string{"network"}, wantErr: "--context"},
		{name: "contexte vide", args: []string{"network", "--context", "  "}, wantErr: "--context"},
		{name: "timeout nul", args: []string{"network", "--context", "k3s-test", "--timeout", "0s"}, wantErr: "--timeout"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := runDoctor(test.args)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("erreur %v sans %q", err, test.wantErr)
			}
		})
	}
}

func TestDoctorClientUsesTheNamedContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	content := `apiVersion: v1
kind: Config
clusters:
- name: dev
  cluster:
    server: https://dev.example:6443
- name: production
  cluster:
    server: https://prod.example:443
contexts:
- name: dev
  context:
    cluster: dev
    user: dev
- name: production
  context:
    cluster: production
    user: prod
current-context: dev
users:
- name: dev
  user: {}
- name: prod
  user: {}
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, config, err := doctorClient(path, "production")
	if err != nil {
		t.Fatal(err)
	}
	if config.Host != "https://prod.example:443" {
		t.Fatalf("contexte courant utilisé: %s", config.Host)
	}
	if _, _, err := doctorClient(path, "absent"); err == nil {
		t.Fatal("un contexte absent a été accepté")
	}
}
