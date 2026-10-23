package control

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/promote"
	"git.host.bzh/pepe/kuberpack/internal/store"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Exercise a real client-go HTTP log stream rather than the fake client's
// GetLogs stub: query forwarding, chunking and cancellation matter here.
func runtimeLogsFixture(t *testing.T, replicas int, logs http.HandlerFunc) http.Handler {
	t.Helper()
	controller := true
	owner := func(kind string, uid types.UID) []metav1.OwnerReference {
		return []metav1.OwnerReference{{Kind: kind, UID: uid, Controller: &controller}}
	}
	d := statusDeployment()
	d.Spec.Template.Spec.InitContainers = []corev1.Container{{Name: "setup"}}
	sets := appsv1.ReplicaSetList{Items: []appsv1.ReplicaSet{
		{ObjectMeta: metav1.ObjectMeta{Name: "ours", UID: "ours", OwnerReferences: owner("Deployment", d.UID)}},
		{ObjectMeta: metav1.ObjectMeta{Name: "foreign", UID: "foreign", OwnerReferences: owner("Deployment", "foreign")}},
	}}
	makePod := func(name string, uid types.UID) corev1.Pod {
		return corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "custom", OwnerReferences: owner("ReplicaSet", uid)}, Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "app", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 17}}}}, InitContainerStatuses: []corev1.ContainerStatus{{Name: "setup", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}}}}}}
	}
	pods := corev1.PodList{Items: []corev1.Pod{makePod("foreign", "foreign"), makePod("job", "ours"), makePod("deleting", "ours")}}
	pods.Items[1].OwnerReferences = owner("Job", "ours")
	now := metav1.Now()
	pods.Items[2].DeletionTimestamp = &now
	for index := range replicas {
		pods.Items = append(pods.Items, makePod(string(rune('a'+index)), "ours"))
	}
	kubeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("logs mutated the cluster: %s %s", r.Method, r.URL)
		}
		if strings.HasSuffix(r.URL.Path, "/log") {
			if r.URL.Path != "/api/v1/namespaces/custom/pods/a/log" && r.URL.Path != "/api/v1/namespaces/custom/pods/b/log" {
				t.Errorf("read a pod outside the app ownership chain: %s", r.URL.Path)
			}
			logs(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/apis/apps/v1/namespaces/custom/deployments/web":
			_ = json.NewEncoder(w).Encode(d)
		case "/apis/apps/v1/namespaces/custom/replicasets":
			if r.URL.Query().Get("labelSelector") != "app=web" {
				t.Error("missing application selector")
			}
			_ = json.NewEncoder(w).Encode(sets)
		case "/api/v1/namespaces/custom/pods":
			_ = json.NewEncoder(w).Encode(pods)
		default:
			t.Errorf("unexpected Kubernetes access: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(kubeAPI.Close)
	kube, err := kubernetes.NewForConfig(&rest.Config{Host: kubeAPI.URL})
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := New(Config{Store: db, APIToken: "api", Kube: kube})
	if err := s.reconcileApps(context.Background(), "demo", []promote.ProjectApp{{Name: "demo-web", Namespace: "custom", Image: "registry/app@sha256:current"}}); err != nil {
		t.Fatal(err)
	}
	return s.Handler()
}

func TestRuntimeLogsAreAuthenticatedOwnedAndForwardKubernetesOptions(t *testing.T) {
	var requests int
	query := ""
	handler := runtimeLogsFixture(t, 1, func(w http.ResponseWriter, r *http.Request) {
		requests++
		query = r.URL.Query().Encode()
		_, _ = io.WriteString(w, "first\nsecond\n")
	})
	for _, tc := range []struct {
		path, token string
		code        int
	}{
		{"/api/v1/apps/demo-web/logs", "", 401},
		{"/api/v1/apps/missing/logs", "api", 404},
		{"/api/v1/apps/demo-web/logs?tailLines=-2", "api", 400},
		{"/api/v1/apps/demo-web/logs?follow=garbage", "api", 400},
		{"/api/v1/apps/demo-web/logs?namespace=other", "api", 400},
		{"/api/v1/apps/demo-web/logs?container=other", "api", 400},
		{"/api/v1/apps/demo-web/logs?sinceSeconds=0", "api", 400},
		{"/api/v1/apps/demo-web/logs?tailLines=1&tailLines=2", "api", 400},
	} {
		r := httptest.NewRequest("GET", tc.path, nil)
		r.Header.Set("Authorization", "Bearer "+tc.token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatalf("%s: got %d: %s", tc.path, w.Code, w.Body.String())
		}
	}
	if requests != 0 {
		t.Fatal("an invalid or unauthorized request opened pod logs")
	}
	for _, options := range []string{"", "?tailLines=7&sinceSeconds=60&previous=true&timestamps=true", "?tailLines=-1&container=setup"} {
		r := httptest.NewRequest("GET", "/api/v1/apps/demo-web/logs"+options, nil)
		r.Header.Set("Authorization", "Bearer api")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 200 || w.Body.String() != "first\nsecond\n" {
			t.Fatalf("runtime logs: %d %q", w.Code, w.Body.String())
		}
		switch options {
		case "":
			if !strings.Contains(query, "tailLines=100") || !strings.Contains(query, "container=app") {
				t.Fatalf("unexpected defaults: %s", query)
			}
		case "?tailLines=-1&container=setup":
			if strings.Contains(query, "tailLines") || !strings.Contains(query, "container=setup") {
				t.Fatalf("all lines/init container: %s", query)
			}
		default:
			for _, want := range []string{"tailLines=7", "sinceSeconds=60", "previous=true", "timestamps=true"} {
				if !strings.Contains(query, want) {
					t.Fatalf("missing %s: %s", want, query)
				}
			}
		}
	}
}

func TestRuntimeLogFollowHasAGlobalConnectionLimit(t *testing.T) {
	handler := runtimeLogsFixture(t, 1, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	api := httptest.NewServer(handler)
	defer api.Close()
	var followers []*http.Response
	defer func() {
		for _, response := range followers {
			response.Body.Close()
		}
	}()
	for index := 0; index < 9; index++ {
		r, _ := http.NewRequest(http.MethodGet, api.URL+"/api/v1/apps/demo-web/logs?follow=true", nil)
		r.Header.Set("Authorization", "Bearer api")
		response, err := api.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		followers = append(followers, response)
		if index < 8 && response.StatusCode != http.StatusOK {
			t.Fatalf("follower %d: HTTP %d", index, response.StatusCode)
		}
		if index == 8 && (response.StatusCode != http.StatusTooManyRequests || response.Header.Get("Retry-After") == "") {
			t.Fatal("excess follow connection was not rejected")
		}
	}
}

func TestRuntimeLogsMergeOwnedReplicasAndLabelTheirOutput(t *testing.T) {
	handler := runtimeLogsFixture(t, 2, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "one\ntwo\n")
	})
	r := httptest.NewRequest("GET", "/api/v1/apps/demo-web/logs", nil)
	r.Header.Set("Authorization", "Bearer api")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("%d: %s", w.Code, w.Body.String())
	}
	for _, line := range []string{"[a/app] one\n", "[a/app] two\n", "[b/app] one\n", "[b/app] two\n"} {
		if !strings.Contains(w.Body.String(), line) {
			t.Fatalf("missing %q: %s", line, w.Body.String())
		}
	}
}

func TestRuntimeLogsFollowStreamsBeforeEOFAndClosesOnDisconnect(t *testing.T) {
	closed := make(chan struct{})
	handler := runtimeLogsFixture(t, 1, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "live now\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(closed)
	})
	api := httptest.NewServer(handler)
	defer api.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "GET", api.URL+"/api/v1/apps/demo-web/logs?follow=true", nil)
	r.Header.Set("Authorization", "Bearer api")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || line != "live now\n" {
		t.Fatalf("follow buffered until EOF: %q %v", line, err)
	}
	cancel()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("developer disconnect left the Kubernetes stream open")
	}
}

func TestRuntimeLogsDoNotHideUpstreamFailuresOrNoPods(t *testing.T) {
	for _, replicas := range []int{0, 1, 6} {
		handler := runtimeLogsFixture(t, replicas, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "forbidden", http.StatusForbidden)
		})
		r := httptest.NewRequest("GET", "/api/v1/apps/demo-web/logs", nil)
		r.Header.Set("Authorization", "Bearer api")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code < 400 || w.Body.Len() == 0 {
			t.Fatalf("%d replicas gave false success: %d %q", replicas, w.Code, w.Body.String())
		}
	}
}

func TestRuntimeLogsFollowDoesNotWaitForAQuietReplicaToSendHeaders(t *testing.T) {
	quietStarted := make(chan struct{})
	quietClosed := make(chan struct{})
	handler := runtimeLogsFixture(t, 2, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/pods/a/") {
			close(quietStarted)
			// Simulate a quiet kubelet that has not sent response headers yet.
			<-r.Context().Done()
			close(quietClosed)
			return
		}
		_, _ = io.WriteString(w, "busy replica\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	api := httptest.NewServer(handler)
	defer api.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "GET", api.URL+"/api/v1/apps/demo-web/logs?follow=true&tailLines=0", nil)
	r.Header.Set("Authorization", "Bearer api")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || line != "[b/app] busy replica\n" {
		t.Fatalf("quiet replica blocked the live replica: %q %v", line, err)
	}
	select {
	case <-quietStarted:
	case <-ctx.Done():
		t.Fatal("quiet replica was never followed")
	}
	cancel()
	select {
	case <-quietClosed:
	case <-time.After(time.Second):
		t.Fatal("quiet upstream request was left open")
	}
}

func TestRuntimeLogsPropagateAnInterruptedKubeletResponseThroughTrailers(t *testing.T) {
	handler := runtimeLogsFixture(t, 1, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = io.WriteString(w, "partial\n")
	})
	api := httptest.NewServer(handler)
	defer api.Close()
	r, _ := http.NewRequest("GET", api.URL+"/api/v1/apps/demo-web/logs", nil)
	r.Header.Set("Authorization", "Bearer api")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || string(data) != "partial\n" || !strings.Contains(resp.Trailer.Get("X-Kuberpack-Log-Error"), "interrupted") {
		t.Fatalf("lost stream failure: %q %v %+v", data, err, resp.Trailer)
	}
}

func TestRuntimeLogWriterPreservesSplitLinesAndUnboundedLineLengths(t *testing.T) {
	w := httptest.NewRecorder()
	writer := &appLogWriter{writer: w, mu: &sync.Mutex{}, prefix: "[a/app] ", lineStart: true}
	for _, data := range []string{"fir", "st\nsecond\n", strings.Repeat("x", 2*1024*1024)} {
		if n, err := writer.Write([]byte(data)); err != nil || n != len(data) {
			t.Fatalf("write: %d %v", n, err)
		}
	}
	if want := "[a/app] first\n[a/app] second\n[a/app] " + strings.Repeat("x", 2*1024*1024); w.Body.String() != want {
		t.Fatal("log lines were truncated or incorrectly prefixed")
	}
}
