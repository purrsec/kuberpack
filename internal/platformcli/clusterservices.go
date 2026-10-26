package platformcli

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/downloader"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/repo"
)

// clusterService is a service of the cluster that the applications need.
//
// The platform does not work without them: the certificates of the
// applications (cert-manager), the PostgreSQL databases of the add-ons
// (the CloudNativePG operator) and the backups of those databases (the
// barman plugin of the operator).
//
// They are installed here, and not by hand: an installation that asks for
// four steps is not an installation.
type clusterService struct {
	// Name is the name of the Helm release.
	Name string
	// Repo is the repository of the chart.
	Repo string
	// Chart is the name of the chart.
	Chart string
	// Version is the version of the chart. It is pinned: a chart that
	// moves is a cluster that changes under the operator's feet.
	Version string
	// Namespace is the namespace of the release.
	Namespace string
	// Values are the values of the chart.
	Values map[string]any
}

// clusterServices are the services of the cluster that the platform needs.
//
// The versions are the ones the platform is tested with. They are
// overridable, and the operator is told when they change.
func clusterServices() []clusterService {
	return []clusterService{
		{
			Name:      "cert-manager",
			Repo:      "https://charts.jetstack.io",
			Chart:     "cert-manager",
			Version:   "v1.21.2",
			Namespace: "cert-manager",
			Values: map[string]any{
				// The platform issues the certificates of the
				// applications. The webhook is the one of the
				// platform, not the one of the cluster.
				"crds": map[string]any{"enabled": true},
				"global": map[string]any{
					"leaderElection": map[string]any{
						"namespace": "cert-manager",
					},
				},
			},
		},
		{
			Name:  "cloudnative-pg",
			Repo:  "https://cloudnative-pg.github.io/charts",
			Chart: "cloudnative-pg",
			// The version of the chart is not the version of the
			// operator: this one is the 1.30.1.
			Version:   "0.29.1",
			Namespace: "cnpg-system",
		},
		{
			// The plugin of the backups. Since CloudNativePG 1.27, the
			// backups go through it, and it is a plugin of its own.
			Name:      "plugin-barman-cloud",
			Repo:      "https://cloudnative-pg.github.io/charts",
			Chart:     "plugin-barman-cloud",
			Version:   "0.8.1",
			Namespace: "cnpg-system",
		},
	}
}

// fetchChart downloads a chart and loads it.
func fetchChart(settings *cli.EnvSettings, service clusterService) (*chart.Chart, error) {
	chartURL, err := repo.FindChartInRepoURL(service.Repo, service.Chart, service.Version, "", "", "", getter.All(settings))
	if err != nil {
		return nil, fmt.Errorf("the chart %s %s: %w", service.Chart, service.Version, err)
	}
	dir, err := os.MkdirTemp("", "kuberpack-chart-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	dest := filepath.Join(dir, "chart")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, err
	}
	downloader := &downloader.ChartDownloader{
		Out:     os.Stderr,
		Verify:  downloader.VerifyNever,
		Getters: getter.All(settings),
		Options: []getter.Option{},
	}
	saved, _, err := downloader.DownloadTo(chartURL, service.Version, dest)
	if err != nil {
		return nil, fmt.Errorf("the chart %s %s: %w", service.Chart, service.Version, err)
	}
	// The chart is a tarball: it is unpacked here.
	if err := untar(saved, dest); err != nil {
		return nil, fmt.Errorf("the chart %s %s: %w", service.Chart, service.Version, err)
	}
	// The chart may be unpacked in a directory of its own: the loader
	// wants the directory that holds the Chart.yaml.
	root := dest
	if _, err := os.Stat(filepath.Join(dest, "Chart.yaml")); err != nil {
		entries, err := os.ReadDir(dest)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if _, err := os.Stat(filepath.Join(dest, entry.Name(), "Chart.yaml")); err == nil {
				root = filepath.Join(dest, entry.Name())
				break
			}
		}
	}
	loaded, err := loader.Load(root)
	if err != nil {
		return nil, fmt.Errorf("the chart %s %s: %w", service.Chart, service.Version, err)
	}
	return loaded, nil
}

// untar unpacks a chart in a directory.
//
// The paths of the archive are checked: a chart that writes outside of
// its directory is refused.
func untar(src, dest string) error {
	file, err := os.Open(src)
	if err != nil {
		return err
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer reader.Close()
	archive := tar.NewReader(reader)
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dest, filepath.Clean("/"+header.Name))
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, archive); err != nil {
				_ = out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		}
	}
}
