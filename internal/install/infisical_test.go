package install

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInfisicalBootstrapConvergesWithoutDuplicateMemberships(t *testing.T) {
	created, identity, member, human := false, false, false, false
	mutations := 0
	var auth map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer bootstrap-token" {
			t.Error("missing bootstrap authentication")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method != "GET" {
			mutations++
		}
		var body map[string]any
		if r.Body != nil && r.Method != "GET" {
			json.NewDecoder(r.Body).Decode(&body)
		}
		switch r.URL.Path {
		case "/api/v1/identities":
			if r.Method == "POST" {
				if body["role"] != "no-access" {
					t.Error("operator granted organization access")
				}
				identity = true
				w.Write([]byte(`{"identity":{"id":"operator"}}`))
			} else if identity {
				w.Write([]byte(`{"identities":[{"identity":{"id":"operator","name":"test-operator"}}]}`))
			} else {
				w.Write([]byte(`{"identities":[]}`))
			}
		case "/api/v1/projects/slug/test-project":
			if !created {
				w.WriteHeader(404)
			} else {
				w.Write([]byte(`{"id":"project"}`))
			}
		case "/api/v1/projects":
			created = true
			w.Write([]byte(`{"project":{"id":"project"}}`))
		case "/api/v1/auth/kubernetes-auth/identities/operator":
			if r.Method == "GET" {
				if auth == nil {
					w.WriteHeader(404)
				} else {
					json.NewEncoder(w).Encode(map[string]any{"identityKubernetesAuth": auth})
				}
			} else {
				auth = body
				w.Write([]byte(`{}`))
			}
		case "/api/v1/projects/project/memberships/identities/operator":
			if r.Method == "GET" && !member {
				w.WriteHeader(404)
			} else {
				if r.Method == "POST" {
					if body["role"] != "viewer" {
						t.Error("operator must have read-only project access")
					}
					member = true
				}
				w.Write([]byte(`{}`))
			}
		case "/api/v1/projects/project/memberships":
			if r.Method == "POST" {
				human = true
				w.Write([]byte(`{}`))
			} else if human {
				w.Write([]byte(`{"memberships":[{"user":{"username":"admin@example.com"}}]}`))
			} else {
				w.Write([]byte(`{"memberships":[]}`))
			}
		default:
			t.Error("unexpected API call", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	api := InfisicalAPI{URL: server.URL, Token: "bootstrap-token"}
	for run := 0; run < 2; run++ {
		before := mutations
		id, err := api.EnsureIdentity(context.Background(), "org", "test-operator")
		if err != nil {
			t.Fatal(err)
		}
		project, err := api.Configure(context.Background(), "test-project", id, "admin@example.com", "certificate")
		if err != nil || project != "project" {
			t.Fatalf("project=%s err=%v", project, err)
		}
		if run == 1 && mutations != before {
			t.Fatal("second bootstrap mutated Infisical")
		}
	}
}

func TestInfisicalFailureDoesNotLeakCredentialsOrTriggerCreation(t *testing.T) {
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			posts++
		}
		w.WriteHeader(403)
		w.Write([]byte("sensitive-server-error"))
	}))
	defer server.Close()
	_, err := (InfisicalAPI{URL: server.URL, Token: "sensitive-token"}).Configure(context.Background(), "project", "identity", "admin@example.com", "ca")
	if err == nil || strings.Contains(err.Error(), "sensitive") || posts != 0 {
		t.Fatalf("unsafe error=%v posts=%d", err, posts)
	}
}
