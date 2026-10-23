package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCLIRuntimeLogsUseOnlyAPIAndAcceptFlagsAfterTheName(t *testing.T) {
	t.Setenv("KUBECONFIG", "/does-not-exist")
	t.Setenv("KUBERPACK_API_TOKEN", "api")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer api" || r.URL.Path != "/api/v1/apps/demo-web/logs" {
			t.Errorf("unexpected log request: %s", r.URL)
		}
		query := r.URL.Query()
		for key, want := range map[string]string{"tailLines": "12", "sinceSeconds": "2", "previous": "true", "timestamps": "true", "container": "setup"} {
			if query.Get(key) != want {
				t.Errorf("%s=%s, want %s", key, query.Get(key), want)
			}
		}
		_, _ = io.WriteString(w, "application output\n")
	}))
	defer server.Close()
	t.Setenv("KUBERPACK_API_URL", server.URL)
	out, err := captureAppOutput(t, func() error {
		return runApp([]string{"logs", "demo-web", "--tail", "12", "--since", "1500ms", "--previous", "--timestamps", "-c", "setup"})
	})
	if err != nil || out != "application output\n" {
		t.Fatalf("logs: %q %v", out, err)
	}
}

func TestCLIBuildLogsRemainAvailableSeparatelyAndThroughLegacyFlag(t *testing.T) {
	t.Setenv("KUBERPACK_API_TOKEN", "api")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/apps/demo-web/builds":
			_, _ = io.WriteString(w, `{"builds":[{"id":7}]}`)
		case "/api/v1/apps/demo-web/builds/7/logs":
			_, _ = io.WriteString(w, "builder output\n")
		case "/api/v1/apps/image-app/builds":
			_, _ = io.WriteString(w, `{"builds":[]}`)
		default:
			t.Errorf("unexpected build log request: %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("KUBERPACK_API_URL", server.URL)
	for _, args := range [][]string{{"build-logs", "demo-web"}, {"build-logs", "--build", "7", "demo-web"}, {"logs", "demo-web", "--build", "7"}, {"logs", "--build", "latest", "demo-web"}} {
		out, err := captureAppOutput(t, func() error { return runApp(args) })
		if err != nil || out != "builder output\n" {
			t.Fatalf("%v: %q %v", args, out, err)
		}
	}
	_, err := captureAppOutput(t, func() error { return runApp([]string{"build-logs", "image-app"}) })
	if err == nil || !strings.Contains(err.Error(), "published images") || !strings.Contains(err.Error(), "app logs") {
		t.Fatalf("image app needs an actionable explanation: %v", err)
	}
}

func TestCLIRuntimeLogsRejectInvalidOptionsBeforeHTTP(t *testing.T) {
	t.Setenv("KUBERPACK_API_TOKEN", "")
	for _, args := range [][]string{{"web", "--tail", "-2"}, {"web", "--since", "0s"}, {"web", "--since", "-1s"}, {"web", "--container", ""}, {"web", "--build", "bad"}, {"web", "--build", "7", "-f"}, {"web", "unexpected"}} {
		err := runAppLogs(args)
		if err == nil || strings.Contains(err.Error(), "API_TOKEN") {
			t.Fatalf("invalid flags not validated: %v: %v", args, err)
		}
	}
}

func TestCLIRuntimeLogsStreamBeforeEOFAndCancelCleanly(t *testing.T) {
	t.Setenv("KUBERPACK_API_TOKEN", "api")
	closed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "live now\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(closed)
	}))
	defer server.Close()
	t.Setenv("KUBERPACK_API_URL", server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	done := make(chan error, 1)
	go func() {
		done <- streamAppLogs(ctx, "/api/v1/apps/demo-web/logs?follow=true", writer)
		writer.Close()
	}()
	data := make([]byte, len("live now\n"))
	if _, err := io.ReadFull(reader, data); err != nil || string(data) != "live now\n" {
		t.Fatalf("CLI buffered the stream: %q %v", data, err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Ctrl-C should finish cleanly: %v", err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not close the API request")
	}
}

func TestCLIRuntimeLogsReportHTTPErrorsAndStreamFailureTrailers(t *testing.T) {
	t.Setenv("KUBERPACK_API_TOKEN", "api")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/denied" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Trailer", "X-Kuberpack-Log-Error")
		_, _ = io.WriteString(w, "partial output\n")
		w.Header().Set("X-Kuberpack-Log-Error", "upstream interrupted")
	}))
	defer server.Close()
	t.Setenv("KUBERPACK_API_URL", server.URL)
	var out bytes.Buffer
	if err := streamAppLogs(context.Background(), "/denied", &out); err == nil || !strings.Contains(err.Error(), "API 401") || out.Len() != 0 {
		t.Fatalf("HTTP error hidden: %q %v", out.String(), err)
	}
	if err := streamAppLogs(context.Background(), "/broken", &out); err == nil || !strings.Contains(err.Error(), "upstream interrupted") || out.String() != "partial output\n" {
		t.Fatalf("stream error hidden: %q %v", out.String(), err)
	}
}
