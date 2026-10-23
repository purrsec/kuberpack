package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func openAPITokenStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kuberpack.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

func TestAPITokenLifecycleKeepsOnlyTheHash(t *testing.T) {
	s, path := openAPITokenStore(t)
	ctx := context.Background()

	token, presented, err := s.CreateAPIToken(ctx, "ci", "demo", APITokenRoleProject, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if token.ID == 0 || token.Project != "demo" || token.Role != APITokenRoleProject {
		t.Fatalf("unexpected token: %+v", token)
	}
	id, secret, ok := ParseAPIToken(presented)
	if !ok || id != token.ID || secret == "" {
		t.Fatalf("unexpected token format %q (%d, %q, %v)", presented, id, secret, ok)
	}
	if !strings.HasPrefix(presented, "kp_"+itoa(token.ID)+"_") {
		t.Fatalf("token does not carry its id: %q", presented)
	}
	if FormatAPIToken(token.ID, secret) != presented {
		t.Fatalf("token format is not stable: %q", presented)
	}

	// La base ne contient que l'empreinte SHA-256 du secret.
	var stored string
	if err := s.db.QueryRowContext(ctx, `SELECT token_hash FROM api_tokens WHERE id = ?`, token.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(secret))
	if want := hex.EncodeToString(sum[:]); stored != want {
		t.Fatalf("stored hash %q, want %q", stored, want)
	}
	if stored == secret || strings.Contains(stored, secret) || strings.Contains(stored, presented) {
		t.Fatalf("the secret is stored in clear: %q", stored)
	}
	// Aucun fichier SQLite, y compris le WAL, ne contient le jeton en clair.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		raw, err := os.ReadFile(path + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(secret)) || bytes.Contains(raw, []byte(presented)) {
			t.Fatalf("the token is written in clear in %s", filepath.Base(path+suffix))
		}
	}

	got, err := s.AuthenticateAPIToken(ctx, presented, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != token.ID || got.Project != "demo" {
		t.Fatalf("unexpected authentication: %+v", got)
	}
	if got.LastUsedAt.IsZero() {
		t.Fatal("a successful authentication must record last_used_at")
	}

	// Mauvais secret, identifiant inconnu et format invalide échouent tous.
	now := time.Now().UTC()
	wrongSecret := FormatAPIToken(token.ID, strings.Repeat("a", 64))
	for _, candidate := range []string{
		wrongSecret,
		FormatAPIToken(token.ID+100, secret),
		"kp_not-a-number_" + secret,
		"not-a-token",
		"kp_" + itoa(token.ID) + "_",
	} {
		if _, err := s.AuthenticateAPIToken(ctx, candidate, now); !errors.Is(err, ErrNotFound) {
			t.Fatalf("candidate %q: unexpected error %v", candidate, err)
		}
	}

	list, err := s.ListAPITokens(ctx)
	if err != nil || len(list) != 1 || list[0].ID != token.ID {
		t.Fatalf("list: %+v %v", list, err)
	}
	if list[0].Name != "ci" || list[0].Project != "demo" || list[0].Role != APITokenRoleProject {
		t.Fatalf("list entry: %+v", list[0])
	}
}

func TestAPITokenExpirationAndRevocation(t *testing.T) {
	s, _ := openAPITokenStore(t)
	ctx := context.Background()

	// Une expiration passée est refusée à la création.
	if _, _, err := s.CreateAPIToken(ctx, "old", "demo", APITokenRoleProject, time.Now().Add(-time.Hour)); err == nil {
		t.Fatal("an already expired token must not be created")
	}

	token, presented, err := s.CreateAPIToken(ctx, "short", "demo", APITokenRoleProject, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateAPIToken(ctx, presented, time.Now().UTC()); err != nil {
		t.Fatalf("a valid token must authenticate: %v", err)
	}
	// La date est reculée directement : l'authentification doit la refuser.
	if _, err := s.db.ExecContext(ctx, `UPDATE api_tokens SET expires_at = ? WHERE id = ?`,
		time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), token.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateAPIToken(ctx, presented, time.Now().UTC()); !errors.Is(err, ErrAPITokenExpired) {
		t.Fatalf("expired token: %v", err)
	}

	other, otherPresented, err := s.CreateAPIToken(ctx, "revoked", "demo", APITokenRoleProject, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeAPIToken(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateAPIToken(ctx, otherPresented, time.Now().UTC()); !errors.Is(err, ErrAPITokenRevoked) {
		t.Fatalf("revoked token: %v", err)
	}
	// La révocation est idempotente et un identifiant inconnu est introuvable.
	if err := s.RevokeAPIToken(ctx, other.ID); err != nil {
		t.Fatalf("second revocation: %v", err)
	}
	if err := s.RevokeAPIToken(ctx, other.ID+100); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown token: %v", err)
	}
}

func TestAPITokenRoleAndProjectMustAgree(t *testing.T) {
	s, _ := openAPITokenStore(t)
	ctx := context.Background()
	cases := []struct {
		name    string
		project string
		role    string
	}{
		{name: "", project: "demo", role: APITokenRoleProject},
		{name: "bad", project: "demo", role: APITokenRoleAdmin},
		{name: "bad", project: "", role: APITokenRoleProject},
		{name: "bad", project: "demo", role: "superuser"},
	}
	for _, tc := range cases {
		if _, _, err := s.CreateAPIToken(ctx, tc.name, tc.project, tc.role, time.Time{}); err == nil {
			t.Fatalf("role %q project %q: expected an error", tc.role, tc.project)
		}
	}
	// Un rôle vide est déduit du projet : vide = administrateur.
	admin, _, err := s.CreateAPIToken(ctx, "root", "", "", time.Time{})
	if err != nil || admin.Role != APITokenRoleAdmin || admin.Project != "" {
		t.Fatalf("administrator token: %+v %v", admin, err)
	}
}

func TestAPITokenMigrationIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kuberpack.sqlite")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, presented, err := first.CreateAPIToken(context.Background(), "keep", "demo", APITokenRoleProject, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	// Rouvrir rejoue la migration : la table existe déjà et le jeton reste
	// utilisable.
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if _, err := second.AuthenticateAPIToken(context.Background(), presented, time.Now().UTC()); err != nil {
		t.Fatalf("token lost across a restart: %v", err)
	}
}

func itoa(value int64) string {
	return strconv.FormatInt(value, 10)
}
