package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/control"
)

func TestCLIPrintsResourceUsageAndCumulativeThrottlingThroughAPIOnly(t *testing.T) {
	t.Setenv("KUBECONFIG", "/does-not-exist")
	t.Setenv("KUBERPACK_API_TOKEN", "api")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/apps/demo-web/status" || r.Header.Get("Authorization") != "Bearer api" {
			t.Error("wrong status API access")
		}
		fmt.Fprint(w, `{"name":"demo-web","deployment":{"state":"running","reconciliation":{"state":"ready"},"resources":{"state":"available","source":"metrics.k8s.io","cpu":{"unit":"millicores","usage":250,"request":100,"limit":1000,"percent_of_limit":25},"memory":{"unit":"bytes","usage":134217728,"request":67108864,"limit":268435456,"percent_of_limit":50},"restarts":2,"measured_containers":1,"total_containers":1,"throttling":{"state":"observed","scope":"current_container_lifetime","periods":100,"throttled_periods":10,"percent_of_periods":10,"throttled_seconds":1.5}}}}`)
	}))
	defer server.Close()
	t.Setenv("KUBERPACK_API_URL", server.URL)
	out, err := captureAppOutput(t, func() error { return runAppStatus([]string{"demo-web"}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"250m / 1000m", "25.0% of limit", "128.0Mi / 256.0Mi", "50.0% of limit", "10.0% of CPU periods", "1.50s cumulative since current containers started", "RESTARTS", "2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q: %s", want, out)
		}
	}
	out, err = captureAppOutput(t, func() error { return runAppStatus([]string{"--json", "demo-web"}) })
	if err != nil || !json.Valid([]byte(out)) || !strings.Contains(out, "current_container_lifetime") {
		t.Fatalf("lost JSON counters: %s %v", out, err)
	}
}

func TestCLIShowsMissingMetricsAsUnavailableAndActualZeroAsZero(t *testing.T) {
	var out bytes.Buffer
	printAppResources(&out, &control.AppResources{State: "unavailable", CPU: control.ResourceReading{Unit: "millicores"}, Memory: control.ResourceReading{Unit: "bytes"}, Throttling: control.ThrottlingStatus{State: "unavailable", Message: "read denied"}})
	if strings.Contains(out.String(), "0m /") || strings.Contains(out.String(), "0.0Mi /") || !strings.Contains(out.String(), "unavailable") {
		t.Fatalf("fabricated zero usage: %s", out.String())
	}
	zero := int64(0)
	if got := formatResourceReading(control.ResourceReading{Unit: "millicores", Usage: &zero}); !strings.HasPrefix(got, "0m /") {
		t.Fatalf("valid zero lost: %s", got)
	}
	out.Reset()
	stamp := time.Date(2026, 10, 22, 19, 0, 0, 0, time.UTC)
	printAppResources(&out, &control.AppResources{State: "partial", Containers: []control.ContainerResources{{Pod: "one", Name: "app", SampledAt: &stamp, WindowSeconds: 15}, {Pod: "two", Name: "app"}}})
	for _, want := range []string{"one/app", "two/app", "2026-10-22T19:00:00Z", "15.0s window"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing replica details %q: %s", want, out.String())
		}
	}
}
