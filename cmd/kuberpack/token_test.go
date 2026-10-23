package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestCLITokenCreateListRevoke(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	var creates []map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer api" {
			t.Errorf("missing API authentication on %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/tokens":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("create body: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			mu.Lock()
			creates = append(creates, body)
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 3, "name": "ci", "project": "demo", "role": "project",
				"token":      "kp_3_" + strings.Repeat("a", 64),
				"created_at": "2026-10-23T10:00:00Z",
				"expires_at": "2026-11-23T10:00:00Z",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/tokens":
			_ = json.NewEncoder(w).Encode(map[string]any{"tokens": []any{
				map[string]any{
					"id": 3, "name": "ci", "project": "demo", "role": "project",
					"status": "active", "created_at": "2026-10-23T10:00:00Z",
					"expires_at": "2026-11-23T10:00:00Z",
				},
			}})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/tokens/3":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 3, "status": "revoked"})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("KUBERPACK_API_URL", server.URL)
	t.Setenv("KUBERPACK_API_TOKEN", "api")

	output, err := captureAppOutput(t, func() error {
		return runToken([]string{"create", "--name", "ci", "--project", "demo", "--expires", "720h"})
	})
	if err != nil {
		t.Fatal(err)
	}
	secret := "kp_3_" + strings.Repeat("a", 64)
	if !strings.Contains(output, "TOKEN") || !strings.Contains(output, secret) || !strings.Contains(output, "demo") {
		t.Fatalf("create output: %s", output)
	}
	mu.Lock()
	if len(creates) != 1 {
		t.Fatalf("create requests: %d", len(creates))
	}
	body := creates[0]
	mu.Unlock()
	if body["name"] != "ci" || body["project"] != "demo" || body["expires_in"] != "720h" {
		t.Fatalf("create body: %v", body)
	}

	output, err = captureAppOutput(t, func() error { return runToken([]string{"list"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "NAME") || !strings.Contains(output, "ci") || !strings.Contains(output, "active") {
		t.Fatalf("list output: %s", output)
	}
	if strings.Contains(output, secret) {
		t.Fatalf("the list prints the secret: %s", output)
	}

	output, err = captureAppOutput(t, func() error { return runToken([]string{"revoke", "3"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "3") {
		t.Fatalf("revoke output: %s", output)
	}
	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, path := range paths {
		if path == "DELETE /api/v1/tokens/3" {
			found = true
		}
	}
	if !found {
		t.Fatalf("requests: %v", paths)
	}
}

func TestCLITokenValidatesArgumentsWithoutNetwork(t *testing.T) {
	t.Setenv("KUBERPACK_API_TOKEN", "api")
	t.Setenv("KUBERPACK_API_URL", "http://127.0.0.1:1")
	for _, args := range [][]string{
		nil,
		{"unknown"},
		{"create"},
		{"create", "--name", "ci", "--expires", "soon"},
		{"revoke"},
		{"revoke", "abc"},
		{"list", "extra"},
	} {
		if err := runToken(args); err == nil {
			t.Fatalf("args %v: expected an error", args)
		}
	}
}

func TestCLITokenRequiresAnAdministratorToken(t *testing.T) {
	t.Setenv("KUBERPACK_API_TOKEN", "")
	t.Setenv("KUBERPACK_TOKEN", "")
	t.Setenv("KUBERPACK_API_URL", "http://127.0.0.1:1")
	if err := runToken([]string{"list"}); err == nil || !strings.Contains(err.Error(), "KUBERPACK_API_TOKEN") {
		t.Fatalf("unexpected error: %v", err)
	}
}
