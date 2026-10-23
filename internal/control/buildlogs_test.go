package control

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// TestClipLogsKeepsTheEnd checks that the extract of a build keeps the
// end of the output, where the failure is, and that it stays bounded.
func TestClipLogsKeepsTheEnd(t *testing.T) {
	small := "the build went well\n"
	if got := clipLogs(small); got != "the build went well" {
		t.Errorf("the output was changed: %q", got)
	}

	// A long output keeps its end, and says what was dropped.
	big := strings.Repeat("noise\n", 40000) + "the failure is here\n"
	got := clipLogs(big)
	if len(got) > maxLogBytes+200 {
		t.Errorf("the extract is %d bytes, it should stay bounded", len(got))
	}
	if !strings.HasSuffix(got, "the failure is here") {
		t.Errorf("the end of the output was lost:\n%s", got[len(got)-200:])
	}
	if !strings.Contains(got, "bytes of output were dropped") {
		t.Error("the extract does not say what it dropped")
	}
}

func TestBuildLogReadIsBoundedEvenForAnOversizedLine(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limitBytes") != strconv.Itoa(maxLogBytes) {
			t.Error("kubelet was not asked for a bounded extract")
		}
		// Deliberately ignore the requested limit, as a large line may exceed
		// the kubelet's approximate bound. No unbounded read may reach SQLite.
		_, _ = io.WriteString(w, strings.Repeat("x", 4*maxLogBytes))
	}))
	defer api.Close()
	kube, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL})
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{Kube: kube})
	logs := s.podLogs(context.Background(), corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "build", Namespace: "build"}})
	if logs == "" || len(logs) > maxLogBytes+200 {
		t.Fatalf("unbounded or empty build extract: %d bytes", len(logs))
	}
}

// TestClipLogsKeepsTheLinesWhole checks that the extract does not start
// in the middle of a line.
func TestClipLogsKeepsTheLinesWhole(t *testing.T) {
	big := strings.Repeat("a line of the build\n", 20000)
	got := clipLogs(big)
	if !strings.HasPrefix(got, "...") {
		t.Error("the extract does not start with the notice")
	}
	after := got[strings.Index(got, "\n")+1:]
	if !strings.HasPrefix(after, "a line of the build") {
		t.Errorf("the extract starts in the middle of a line: %q", after[:40])
	}
}
