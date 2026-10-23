package control

import (
	"context"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/store"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

var podMetricsGVR = schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}

func resourceFixture(t *testing.T, replicas int) (*Server, store.App, []corev1.Pod, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	d := statusDeployment()
	controller := true
	owner := func(kind string, uid types.UID) []metav1.OwnerReference {
		return []metav1.OwnerReference{{Kind: kind, UID: uid, Controller: &controller}}
	}
	set := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "set", Namespace: "custom", UID: "ours", Labels: map[string]string{"app": "web"}, OwnerReferences: owner("Deployment", d.UID)}}
	objects := []runtime.Object{d, set}
	var pods []corev1.Pod
	for index := range replicas {
		name := string(rune('a' + index))
		pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "custom", UID: types.UID(name), CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour)), Labels: map[string]string{"app": "web"}, OwnerReferences: owner("ReplicaSet", set.UID)}, Spec: corev1.PodSpec{NodeName: "node", Containers: []corev1.Container{{Name: "app", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("64Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("256Mi")}}}}}, Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "app", ContainerID: "containerd://current-" + name, RestartCount: 2, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}}
		pods = append(pods, pod)
		objects = append(objects, pod.DeepCopy())
	}
	foreign := pods[0].DeepCopy()
	foreign.Name = "foreign"
	foreign.OwnerReferences = owner("Job", set.UID)
	objects = append(objects, foreign)
	kube := fake.NewSimpleClientset(objects...)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{podMetricsGVR: "PodMetricsList"})
	for _, pod := range append(pods, *foreign) {
		metric := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "metrics.k8s.io/v1beta1", "kind": "PodMetrics", "metadata": map[string]any{"name": pod.Name, "namespace": "custom", "labels": map[string]any{"app": "web"}}, "timestamp": time.Now().Add(-5 * time.Second).UTC().Format(time.RFC3339Nano), "window": "15s", "containers": []any{map[string]any{"name": "app", "usage": map[string]any{"cpu": "125000000n", "memory": "128Mi"}}}}}
		if _, err := dyn.Resource(podMetricsGVR).Namespace("custom").Create(context.Background(), metric, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	return New(Config{Kube: kube, KubeDynamic: dyn}), store.App{Name: "demo-web", Project: "demo", Namespace: "custom"}, pods, dyn
}

func installTestKubelet(t *testing.T, s *Server, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	endpoint, _ := url.Parse(server.URL)
	host, portText, _ := net.SplitHostPort(endpoint.Host)
	port, _ := strconv.Atoi(portText)
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node"}, Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: host}}, DaemonEndpoints: corev1.NodeDaemonEndpoints{KubeletEndpoint: corev1.DaemonEndpoint{Port: int32(port)}}}}
	if _, err := s.cfg.Kube.CoreV1().Nodes().Create(context.Background(), node, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	s.cfg.KubeConfig = &rest.Config{Host: "https://api.example.org", BearerToken: "token", TLSClientConfig: rest.TLSClientConfig{CAData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})}}
	return server
}

func cfsText(pod, id string, periods, throttled, seconds float64) string {
	var out strings.Builder
	for index, name := range []string{"periods_total", "throttled_periods_total", "throttled_seconds_total"} {
		value := []float64{periods, throttled, seconds}[index]
		fmt.Fprintf(&out, "container_cpu_cfs_%s{namespace=\"custom\",pod=%q,container=\"app\",name=%q} %g\n", name, pod, id, value)
	}
	return out.String()
}

func TestResourceStatusAggregatesOnlyOwnedPodsAndCurrentContainerCounters(t *testing.T) {
	s, app, _, _ := resourceFixture(t, 2)
	installTestKubelet(t, s, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics/cadvisor" || r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("unexpected kubelet request: %s", r.URL)
		}
		fmt.Fprint(w, cfsText("a", "current-a", 100, 20, 3))
		fmt.Fprint(w, cfsText("b", "current-b", 900, 30, 7))
		fmt.Fprint(w, cfsText("a", "old-instance", 1000, 1000, 900))
		fmt.Fprint(w, cfsText("foreign", "current-foreign", 1000, 1000, 900))
	})
	out := s.observeAppResources(context.Background(), app)
	if out.State != "available" || out.TotalContainers != 2 || out.MeasuredContainers != 2 || *out.CPU.Usage != 250 || out.CPU.Request != 200 || *out.CPU.Limit != 2000 || *out.CPU.PercentOfLimit != 12.5 || *out.Memory.Usage != 256*1024*1024 || *out.Memory.PercentOfLimit != 50 || out.Restarts != 4 {
		t.Fatalf("unexpected resource summary: %+v", out)
	}
	if out.Throttling.State != "observed" || *out.Throttling.PercentOfPeriods != 5 || *out.Throttling.ThrottledSeconds != 10 || out.Throttling.Scope != "current_container_lifetime" {
		t.Fatalf("wrong counter scope or average: %+v", out.Throttling)
	}
	for _, action := range s.cfg.Kube.(*fake.Clientset).Actions() {
		resource := action.GetResource().Resource
		if resource == "secrets" || action.GetSubresource() == "exec" || action.GetSubresource() == "proxy" {
			t.Fatalf("resource observation made unrelated access: %+v", action)
		}
	}
}

func TestPartialStaleAndMalformedMetricsNeverBecomeZeroUsage(t *testing.T) {
	for _, change := range []string{"missing", "stale", "future", "negative", "invalid", "old pod", "old container"} {
		t.Run(change, func(t *testing.T) {
			s, app, _, dyn := resourceFixture(t, 2)
			metric, err := dyn.Resource(podMetricsGVR).Namespace("custom").Get(context.Background(), "a", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "missing":
				_ = dyn.Resource(podMetricsGVR).Namespace("custom").Delete(context.Background(), "a", metav1.DeleteOptions{})
			case "stale":
				metric.Object["timestamp"] = time.Now().Add(-5 * time.Minute).Format(time.RFC3339)
			case "future":
				metric.Object["timestamp"] = time.Now().Add(5 * time.Minute).Format(time.RFC3339)
			case "negative":
				metric.Object["containers"] = []any{map[string]any{"name": "app", "usage": map[string]any{"cpu": "-1", "memory": "128Mi"}}}
			case "invalid":
				metric.Object["containers"] = []any{map[string]any{"name": "app", "usage": map[string]any{"cpu": "oops", "memory": "128Mi"}}}
			case "old pod":
				metric.Object["timestamp"] = time.Now().Add(-time.Hour * 2).Format(time.RFC3339)
			case "old container":
				pod, _ := s.cfg.Kube.CoreV1().Pods("custom").Get(context.Background(), "a", metav1.GetOptions{})
				pod.Status.ContainerStatuses[0].State.Running.StartedAt = metav1.Now()
				_, _ = s.cfg.Kube.CoreV1().Pods("custom").Update(context.Background(), pod, metav1.UpdateOptions{})
			}
			if change != "missing" {
				_, _ = dyn.Resource(podMetricsGVR).Namespace("custom").Update(context.Background(), metric, metav1.UpdateOptions{})
			}
			out := s.observeAppResources(context.Background(), app)
			if out.State != "partial" || out.MeasuredContainers != 1 || out.CPU.Usage != nil || out.Memory.Usage != nil || out.CPU.PercentOfLimit != nil || out.Containers[0].CPU.Usage != nil || out.Containers[1].CPU.Usage == nil {
				t.Fatalf("partial metrics fabricated usage: %+v", out)
			}
		})
	}
}

func TestKubeletFailureDoesNotRemoveCPUAndMemoryOrDisableTLSVerification(t *testing.T) {
	for _, failure := range []string{"forbidden", "untrusted", "disabled verification in api config", "old container", "missing family", "malformed"} {
		t.Run(failure, func(t *testing.T) {
			s, app, _, _ := resourceFixture(t, 1)
			installTestKubelet(t, s, func(w http.ResponseWriter, r *http.Request) {
				switch failure {
				case "forbidden":
					http.Error(w, "forbidden", http.StatusForbidden)
				case "old container":
					fmt.Fprint(w, cfsText("a", "old-a", 100, 90, 10))
				case "missing family":
					fmt.Fprint(w, "container_cpu_cfs_periods_total{namespace=\"custom\",pod=\"a\",container=\"app\",name=\"current-a\"} 100\n")
				case "malformed":
					fmt.Fprint(w, "broken { data\n")
				default:
					fmt.Fprint(w, cfsText("a", "current-a", 100, 10, 1))
				}
			})
			if failure == "untrusted" || failure == "disabled verification in api config" {
				s.cfg.KubeConfig.CAData = nil
				s.cfg.KubeConfig.Insecure = failure == "disabled verification in api config"
			}
			out := s.observeAppResources(context.Background(), app)
			if out.State != "available" || out.CPU.Usage == nil || out.Throttling.State != "unavailable" || out.Throttling.PercentOfPeriods != nil {
				t.Fatalf("failed metrics yielded a false success: %+v", out)
			}
		})
	}
}

func TestMissingMetricsAPIKeepsLimitsAndDoesNotChangeDeploymentHealth(t *testing.T) {
	s, app, _, _ := resourceFixture(t, 1)
	s.cfg.KubeDynamic = nil
	out := s.observeAppResources(context.Background(), app)
	if out.State != "unavailable" || out.CPU.Usage != nil || out.Memory.Usage != nil || out.CPU.Limit == nil || *out.CPU.Limit != 1000 || *out.Memory.Limit != 256*1024*1024 {
		t.Fatalf("missing source fabricated usage or lost configuration: %+v", out)
	}
}

func TestCFSCounterParsingRejectsStaleNonFiniteAndDuplicateSeries(t *testing.T) {
	for _, text := range []string{
		strings.ReplaceAll(cfsText("a", "current-a", 100, 1, 1), "} 1\n", "} NaN\n"),
		cfsText("a", "current-a", 100, 1, -1),
		strings.ReplaceAll(cfsText("a", "current-a", 100, 1, 1), "\n", fmt.Sprintf(" %d\n", time.Now().Add(-5*time.Minute).UnixMilli())),
		cfsText("a", "current-a", 100, 1, 1) + cfsText("a", "current-a", 100, 1, 1),
	} {
		counters, err := parseCFSCounters([]byte(text), time.Now())
		if err == nil && !counters["custom/a/app/current-a"].invalid {
			t.Fatalf("accepted invalid CFS sample: %s", text)
		}
	}
	text := strings.ReplaceAll(cfsText("a", "current-a", 0, 0, 0), "name=\"current-a\"", "id=\"/kubepods/cri-containerd-current-a.scope\"")
	counters, err := parseCFSCounters([]byte(text), time.Now())
	if err != nil || counters["custom/a/app/current-a"].periods == nil || *counters["custom/a/app/current-a"].periods != 0 {
		t.Fatalf("lost valid zero or cgroup instance ID: %+v %v", counters, err)
	}
}
