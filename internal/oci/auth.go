package oci

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// The registry speaks two authentication schemes.
//
// A registry that serves its own accounts (a Forgejo, a GitLab) accepts the
// credentials directly:
//
//	Authorization: Basic base64(user:token)
//
// A hosted registry (GHCR, ECR, GAR, Docker Hub) refuses it and answers 401
// with the address of a token service:
//
//	www-authenticate: Bearer realm="https://ghcr.io/token",service="ghcr.io"
//
// The client then exchanges the credentials against that service and retries
// with the token it gets. It is the standard flow of the registry API, the
// same one `docker login` uses.

// bearerChallenge is the authentication challenge of a registry that wants a
// token.
type bearerChallenge struct {
	realm   string
	service string
	scope   string
}

// parseChallenge reads the Www-Authenticate header of a registry.
func parseChallenge(header string) (bearerChallenge, error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return bearerChallenge{}, fmt.Errorf("the registry did not ask for a token")
	}
	// The scheme is "Bearer", the parameters are quoted pairs.
	if !strings.HasPrefix(strings.ToLower(header), "bearer ") {
		return bearerChallenge{}, fmt.Errorf("unsupported authentication scheme: %s", header)
	}
	out := bearerChallenge{}
	for _, part := range splitParams(header[len("Bearer "):]) {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"`)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "realm":
			out.realm = value
		case "service":
			out.service = value
		case "scope":
			out.scope = value
		}
	}
	if out.realm == "" {
		return bearerChallenge{}, fmt.Errorf("the registry did not give a token service: %s", header)
	}
	return out, nil
}

// splitParams splits a list of comma separated parameters, keeping the
// commas that are inside a quoted value.
func splitParams(raw string) []string {
	var out []string
	var current strings.Builder
	inQuotes := false
	for _, r := range raw {
		switch {
		case r == '"':
			inQuotes = !inQuotes
			current.WriteRune(r)
		case r == ',' && !inQuotes:
			out = append(out, current.String())
			current.Reset()
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		out = append(out, current.String())
	}
	return out
}

// exchange trades the credentials of the platform for a token of the
// registry.
func (r *Registry) exchange(ctx context.Context, challenge bearerChallenge) (string, error) {
	target, err := url.Parse(challenge.realm)
	if err != nil {
		return "", fmt.Errorf("token service %q: %w", challenge.realm, err)
	}
	query := target.Query()
	if challenge.service != "" {
		query.Set("service", challenge.service)
	}
	if challenge.scope != "" {
		query.Set("scope", challenge.scope)
	}
	target.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return "", err
	}
	user := r.User
	if user == "" {
		user = "token"
	}
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+r.Token)))
	req.Header.Set("Accept", "application/json")

	httpClient := r.client()
	res, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxBody+1))
	if err != nil {
		return "", err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return "", fmt.Errorf("the token service answered %s", res.Status)
	}
	var payload struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", fmt.Errorf("decode the token: %w", err)
	}
	token := payload.Token
	if token == "" {
		token = payload.AccessToken
	}
	if token == "" {
		return "", fmt.Errorf("the token service did not give a token")
	}
	return token, nil
}
