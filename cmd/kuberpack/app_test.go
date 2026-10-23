package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func captureAppOutput(t *testing.T, run func() error) (string, error) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = original; reader.Close(); writer.Close() }()
	err = run()
	writer.Close()
	os.Stdout = original
	data, readErr := io.ReadAll(reader)
	if readErr != nil {
		t.Fatal(readErr)
	}
	return string(data), err
}

func TestCLIShowsObservedStateAndReplicaCountsThroughTheAPIOnly(t *testing.T) {
	t.Setenv("KUBECONFIG", "/does-not-exist")
	t.Setenv("KUBERPACK_API_TOKEN", "api")
	app := map[string]any{"name": "demo-web", "project": "demo", "hostname": "web.example.org", "deployment": map[string]any{"state": "failed", "reason": "CrashLoopBackOff", "message": "Container app cannot start (CrashLoopBackOff)", "replicas": map[string]int{"desired": 2, "ready": 1, "available": 1, "updated": 2}, "reconciliation": map[string]string{"state": "ready"}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer api" {
			t.Error("missing API authentication")
		}
		switch r.URL.Path {
		case "/api/v1/apps":
			_ = json.NewEncoder(w).Encode(map[string]any{"apps": []any{app}})
		case "/api/v1/apps/demo-web/status":
			_ = json.NewEncoder(w).Encode(app)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("KUBERPACK_API_URL", server.URL)
	output, err := captureAppOutput(t, func() error { return runApps(nil) })
	if err != nil || !strings.Contains(output, "STATUS") || !strings.Contains(output, "failed") || !strings.Contains(output, "1/2") {
		t.Fatalf("apps: %s %v", output, err)
	}
	output, err = captureAppOutput(t, func() error { return runApp([]string{"status", "demo-web"}) })
	if err != nil || !strings.Contains(output, "CrashLoopBackOff") || !strings.Contains(output, "AVAILABLE") || !strings.Contains(output, "1/2") {
		t.Fatalf("status: %s %v", output, err)
	}
	for _, run := range []func() error{func() error { return runApps([]string{"--json"}) }, func() error { return runApp([]string{"status", "--json", "demo-web"}) }} {
		output, err = captureAppOutput(t, run)
		if err != nil || !json.Valid([]byte(output)) {
			t.Fatalf("JSON: %s %v", output, err)
		}
	}
}

func TestCLILeavesReadinessUnknownWhenTheServerHasNoObservation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"apps":[{"name":"legacy","project":"demo"}]}`))
	}))
	defer server.Close()
	t.Setenv("KUBERPACK_API_URL", server.URL)
	t.Setenv("KUBERPACK_API_TOKEN", "api")
	output, err := captureAppOutput(t, func() error { return runApps(nil) })
	if err != nil || !strings.Contains(output, "unknown") || strings.Contains(output, "0/0") {
		t.Fatalf("legacy server: %s %v", output, err)
	}
}
