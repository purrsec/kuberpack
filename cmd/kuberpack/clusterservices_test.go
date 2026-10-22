package main

import (
	"os"
	"testing"

	"helm.sh/helm/v3/pkg/cli"
)

// The services of the cluster are pinned: the chart of every version the
// platform asks for must exist and must be the one it names.
func TestTheClusterServicesAreFetchable(t *testing.T) {
	if os.Getenv("KUBERPACK_TEST_NETWORK") == "" {
		t.Skip("set KUBERPACK_TEST_NETWORK to fetch the charts")
	}
	settings := cli.New()
	for _, service := range clusterServices() {
		chart, err := fetchChart(settings, service)
		if err != nil {
			t.Fatalf("the chart %s %s: %v", service.Chart, service.Version, err)
		}
		if chart.Metadata.Version != service.Version {
			t.Errorf("the chart %s is the version %s, want %s",
				service.Chart, chart.Metadata.Version, service.Version)
		}
	}
}
