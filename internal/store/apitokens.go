package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Un jeton API est montré une seule fois, à sa création. Seule son empreinte
// SHA-256 est conservée : une copie de la base ne permet pas de rejouer un
// jeton, et aucun journal ne contient le secret.
const (
	// APITokenPrefix distingue les jetons de Kuberpack des autres credentials.
	APITokenPrefix = "kp_"
	// APITokenRoleAdmin donne l'administration de la plateforme.
	APITokenRoleAdmin = "admin"
	// APITokenRoleProject limite un jeton à un projet.
	APITokenRoleProject    = "project"
	APITokenRoleReader     = "reader"
	APITokenRoleDeployer   = "deployer"
	APITokenRoleMaintainer = "maintainer"

	apiTokenSecretBytes = 32
	maxAPITokenSecret   = 512
)

var (
	// ErrAPITokenExpired est renvoyé par AuthenticateAPIToken quand la date
	// d'expiration est passée. Le middleware le traduit en 401, sans dire au
	// client lequel des contrôles a échoué.
	ErrAPITokenExpired = errors.New("api token expired")
	// ErrAPITokenRevoked est renvoyé pour un jeton révoqué.
	ErrAPITokenRevoked = errors.New("api token revoked")
)

// APIToken est la fiche publique d'un jeton. Elle ne contient jamais
// l'empreinte : l'API et la CLI ne peuvent donc pas la divulguer.
type APIToken struct {
	ID         int64
	Name       string
	Project    string
	Role       string
	Services   []string
	CreatedAt  time.Time
	LastUsedAt time.Time
	ExpiresAt  time.Time
	RevokedAt  time.Time
}

// dummyAPITokenHash sert à comparer en temps constant même quand l'identifiant
// du jeton n'existe pas : un attaquant ne peut pas distinguer un identifiant
// inconnu d'un secret invalide par le temps de réponse.
const dummyAPITokenHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// NewAPITokenSecret tire un secret de 32 octets aléatoires, encodé en
// hexadécimal. Il n'est jamais écrit en clair.
func NewAPITokenSecret() (string, error) {
	var raw [apiTokenSecretBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

// hashAPITokenSecret calcule l'empreinte SHA-256 du secret. Le sel est inutile
// ici : le secret est déjà entropique, et l'empreinte doit rester vérifiable
// sans stocker autre chose que le hash.
func hashAPITokenSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// ParseAPIToken découpe le format kp_<id>_<secret>. L'identifiant permet de
// retrouver la ligne sans parcourir la table ; le secret reste comparé à
// l'empreinte stockée en temps constant.
func ParseAPIToken(token string) (int64, string, bool) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, APITokenPrefix) {
		return 0, "", false
	}
	idPart, secret, ok := strings.Cut(strings.TrimPrefix(token, APITokenPrefix), "_")
	if !ok || idPart == "" || secret == "" || len(secret) > maxAPITokenSecret {
		return 0, "", false
	}
	id, err := strconv.ParseInt(idPart, 10, 64)
	if err != nil || id <= 0 {
		return 0, "", false
	}
	return id, secret, true
}

// FormatAPIToken assemble le jeton présenté au client.
func FormatAPIToken(id int64, secret string) string {
	return fmt.Sprintf("%s%d_%s", APITokenPrefix, id, secret)
}

// CreateAPIToken enregistre un jeton et renvoie sa fiche et sa valeur en
// clair. La valeur n'est affichée qu'à cet instant : elle n'est pas
// récupérable ensuite.
func (s *Store) CreateAPIToken(ctx context.Context, name, project, role string, expiresAt time.Time) (APIToken, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return APIToken{}, "", fmt.Errorf("the token name is required")
	}
	if len(name) > 120 {
		return APIToken{}, "", fmt.Errorf("the token name is too long")
	}
	project = strings.TrimSpace(project)
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" {
		if project == "" {
			role = APITokenRoleAdmin
		} else {
			role = APITokenRoleProject
		}
	}
	switch role {
	case APITokenRoleAdmin:
		if project != "" {
			return APIToken{}, "", fmt.Errorf("an administrator token cannot be scoped to a project")
		}
	case APITokenRoleProject, APITokenRoleReader, APITokenRoleDeployer, APITokenRoleMaintainer:
		if project == "" {
			return APIToken{}, "", fmt.Errorf("a project token requires a project")
		}
	default:
		return APIToken{}, "", fmt.Errorf("unknown token role %q", role)
	}

	secret, err := NewAPITokenSecret()
	if err != nil {
		return APIToken{}, "", err
	}
	now := time.Now().UTC()
	expires := ""
	if !expiresAt.IsZero() {
		expiresAt = expiresAt.UTC()
		if !expiresAt.After(now) {
			return APIToken{}, "", fmt.Errorf("the expiration must be in the future")
		}
		expires = expiresAt.Format(time.RFC3339Nano)
	}
	res, err := s.db.ExecContext(ctx, `
INSERT INTO api_tokens (name, token_hash, project, role, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		name, hashAPITokenSecret(secret), project, role, now.Format(time.RFC3339Nano), expires,
	)
	if err != nil {
		return APIToken{}, "", err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return APIToken{}, "", err
	}
	created, err := s.APITokenByID(ctx, id)
	if err != nil {
		// La fiche est illisible (erreur SQLite) : la ligne ne doit pas
		// rester sans que son secret ait été montré.
		_, _ = s.db.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ?`, id)
		return APIToken{}, "", err
	}
	// Le jeton n'est assemblé qu'après l'insertion : l'identifiant vient de
	// SQLite et fait partie du format montré à l'utilisateur.
	return created, FormatAPIToken(id, secret), nil
}

// APITokenByID relit la fiche publique d'un jeton.
func (s *Store) APITokenByID(ctx context.Context, id int64) (APIToken, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, name, project, role, created_at, last_used_at, expires_at, revoked_at, services
FROM api_tokens WHERE id = ?`, id)
	return scanAPIToken(row)
}

// AuthenticateAPIToken résout un jeton présenté. Le secret est d'abord
// comparé à l'empreinte en temps constant, puis l'état du jeton est vérifié :
// un client sans le secret n'apprend pas si un jeton existe, est expiré ou a
// été révoqué.
func (s *Store) AuthenticateAPIToken(ctx context.Context, presented string, now time.Time) (APIToken, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	id, secret, ok := ParseAPIToken(presented)
	if !ok {
		burnAPITokenComparison(hashAPITokenSecret(presented))
		return APIToken{}, ErrNotFound
	}
	var storedHash string
	var token APIToken
	var created, lastUsed, expires, revoked, services string
	err := s.db.QueryRowContext(ctx, `
SELECT id, name, token_hash, project, role, created_at, last_used_at, expires_at, revoked_at, services
FROM api_tokens WHERE id = ?`, id,
	).Scan(&token.ID, &token.Name, &storedHash, &token.Project, &token.Role, &created, &lastUsed, &expires, &revoked, &services)
	if errors.Is(err, sql.ErrNoRows) {
		burnAPITokenComparison(hashAPITokenSecret(secret))
		return APIToken{}, ErrNotFound
	}
	if err != nil {
		return APIToken{}, err
	}
	if subtle.ConstantTimeCompare([]byte(storedHash), []byte(hashAPITokenSecret(secret))) != 1 {
		return APIToken{}, ErrNotFound
	}
	if err := json.Unmarshal([]byte(services), &token.Services); err != nil {
		return APIToken{}, err
	}
	token.CreatedAt = parseAPITokenTime(created)
	token.LastUsedAt = parseAPITokenTime(lastUsed)
	token.ExpiresAt = parseAPITokenTime(expires)
	token.RevokedAt = parseAPITokenTime(revoked)
	if !token.RevokedAt.IsZero() {
		return token, ErrAPITokenRevoked
	}
	if !token.ExpiresAt.IsZero() && !now.Before(token.ExpiresAt) {
		return token, ErrAPITokenExpired
	}
	// last_used_at n'est pas un journal : une écriture par minute suffit à
	// savoir qu'un jeton sert encore, sans transformer chaque lecture en
	// écriture SQLite.
	if token.LastUsedAt.IsZero() || now.Sub(token.LastUsedAt) >= time.Minute {
		if _, err := s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, now.Format(time.RFC3339Nano), token.ID); err == nil {
			token.LastUsedAt = now
		}
	}
	return token, nil
}

// ListAPITokens liste les jetons, le plus récent d'abord. Les jetons révoqués
// restent visibles : la trame d'audit ne doit pas disparaître.
func (s *Store) ListAPITokens(ctx context.Context) ([]APIToken, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, project, role, created_at, last_used_at, expires_at, revoked_at, services
FROM api_tokens ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []APIToken
	for rows.Next() {
		token, err := scanAPIToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, token)
	}
	return out, rows.Err()
}

// RevokeAPIToken invalide un jeton. La révocation est idempotente : révoquer
// deux fois ne réactive rien et ne renvoie pas d'erreur.
func (s *Store) RevokeAPIToken(ctx context.Context, id int64) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = ? WHERE id = ? AND revoked_at = ''`, now, id)
	if err != nil {
		return err
	}
	if affected, err := res.RowsAffected(); err == nil && affected > 0 {
		return nil
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM api_tokens WHERE id = ?`, id).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return ErrNotFound
	}
	return nil
}

// rowScanner couvre *sql.Row et *sql.Rows : la lecture d'une fiche est la même
// sur une ligne unique et sur une liste.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanAPIToken(row rowScanner) (APIToken, error) {
	var token APIToken
	var created, lastUsed, expires, revoked, services string
	err := row.Scan(&token.ID, &token.Name, &token.Project, &token.Role, &created, &lastUsed, &expires, &revoked, &services)
	if errors.Is(err, sql.ErrNoRows) {
		return APIToken{}, ErrNotFound
	}
	if err != nil {
		return APIToken{}, err
	}
	if err := json.Unmarshal([]byte(services), &token.Services); err != nil {
		return APIToken{}, err
	}
	token.CreatedAt = parseAPITokenTime(created)
	token.LastUsedAt = parseAPITokenTime(lastUsed)
	token.ExpiresAt = parseAPITokenTime(expires)
	token.RevokedAt = parseAPITokenTime(revoked)
	return token, nil
}

// burnAPITokenComparison fait un travail de comparaison équivalent quand le
// jeton n'existe pas.
func burnAPITokenComparison(computed string) {
	_ = subtle.ConstantTimeCompare([]byte(dummyAPITokenHash), []byte(computed))
}

func parseAPITokenTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func (s *Store) SetAPITokenServices(ctx context.Context, id int64, services []string) error {
	b, err := json.Marshal(services)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE api_tokens SET services=? WHERE id=?`, string(b), id)
	return err
}
