package install

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// InfisicalAPI configures the application after Helm bootstraps its database.
// Error messages deliberately omit bodies and credentials from this API.
type InfisicalAPI struct {
	URL, Token string
	Client     *http.Client
}

func (a InfisicalAPI) EnsureIdentity(ctx context.Context, organizationID, name string) (string, error) {
	var list struct {
		Identities []struct {
			Identity struct{ ID, Name string } `json:"identity"`
		} `json:"identities"`
	}
	if _, err := a.call(ctx, "GET", "/api/v1/identities?orgId="+url.QueryEscape(organizationID), nil, &list); err != nil {
		return "", err
	}
	for _, entry := range list.Identities {
		if entry.Identity.Name == name {
			return entry.Identity.ID, nil
		}
	}
	var created struct {
		Identity struct{ ID string } `json:"identity"`
	}
	_, err := a.call(ctx, "POST", "/api/v1/identities", map[string]any{"name": name, "organizationId": organizationID, "role": "no-access"}, &created)
	if err != nil {
		return "", err
	}
	if created.Identity.ID == "" {
		return "", fmt.Errorf("Infisical returned an empty identity ID")
	}
	return created.Identity.ID, nil
}

func (a InfisicalAPI) call(ctx context.Context, method, path string, input, output any) (int, error) {
	var data []byte
	var err error
	if input != nil {
		data, err = json.Marshal(input)
		if err != nil {
			return 0, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(a.URL, "/")+path, bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)
	req.Header.Set("Content-Type", "application/json")
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("Infisical %s %s: connection failed", method, path)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return 404, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("Infisical %s %s: HTTP %d", method, path, resp.StatusCode)
	}
	if output != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(output); err != nil {
			return resp.StatusCode, fmt.Errorf("Infisical %s: invalid JSON response", path)
		}
	}
	return resp.StatusCode, nil
}

// Configure is idempotent and grants access only to the existing bootstrap
// identity and the existing local administrator, never to an invited stranger.
func (a InfisicalAPI) Configure(ctx context.Context, slug, identityID, adminEmail, caCert string) (string, error) {
	var project struct {
		ID      string `json:"id"` // lookup by slug returns the project directly
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	status, err := a.call(ctx, "GET", "/api/v1/projects/slug/"+url.PathEscape(slug), nil, &project)
	if err != nil {
		return "", err
	}
	if status == 404 {
		_, err = a.call(ctx, "POST", "/api/v1/projects", map[string]any{"projectName": slug, "slug": slug, "type": "secret-manager", "shouldCreateDefaultEnvs": true}, &project)
		if err != nil {
			return "", err
		}
	}
	if project.ID != "" {
		project.Project.ID = project.ID
	}
	if project.Project.ID == "" {
		return "", fmt.Errorf("Infisical returned an empty project ID")
	}
	projectID := project.Project.ID
	authPath := "/api/v1/auth/kubernetes-auth/identities/" + url.PathEscape(identityID)
	desired := map[string]any{"kubernetesHost": "https://kubernetes.default.svc:443", "caCert": caCert, "allowedNamespaces": "infisical", "allowedNames": "infisical-service-account", "accessTokenTTL": 3600, "accessTokenMaxTTL": 3600}
	var current struct {
		Auth map[string]any `json:"identityKubernetesAuth"`
	}
	status, err = a.call(ctx, "GET", authPath, nil, &current)
	if err != nil {
		return "", err
	}
	if status == 404 {
		_, err = a.call(ctx, "POST", authPath, desired, nil)
	} else {
		changed := false
		for key, value := range desired {
			if fmt.Sprint(current.Auth[key]) != fmt.Sprint(value) {
				changed = true
			}
		}
		if changed {
			_, err = a.call(ctx, "PATCH", authPath, desired, nil)
		}
	}
	if err != nil {
		return "", err
	}
	membershipPath := "/api/v1/projects/" + url.PathEscape(projectID) + "/memberships/identities/" + url.PathEscape(identityID)
	status, err = a.call(ctx, "GET", membershipPath, nil, nil)
	if err != nil {
		return "", err
	}
	if status == 404 {
		if _, err = a.call(ctx, "POST", membershipPath, map[string]any{"role": "viewer"}, nil); err != nil {
			return "", err
		}
	}
	var memberships any
	_, err = a.call(ctx, "GET", "/api/v1/projects/"+url.PathEscape(projectID)+"/memberships", nil, &memberships)
	if err != nil {
		return "", err
	}
	if !containsAccount(memberships, adminEmail) {
		_, err = a.call(ctx, "POST", "/api/v1/projects/"+url.PathEscape(projectID)+"/memberships", map[string]any{"usernames": []string{adminEmail}, "roleSlugs": []string{"admin"}}, nil)
		if err != nil {
			return "", err
		}
	}
	return projectID, nil
}

func containsAccount(value any, account string) bool {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			if (key == "email" || key == "username") && item == account {
				return true
			}
			if containsAccount(item, account) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if containsAccount(item, account) {
				return true
			}
		}
	}
	return false
}
