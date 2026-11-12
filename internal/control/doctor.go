package control

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"git.host.bzh/pepe/kuberpack/internal/api"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// doctorV2 reports what the platform can actually do, before a manifest is
// pushed. Every check here answers a question that otherwise costs a full
// push, a webhook, a compilation and a log dig to learn: does the control
// plane read namespaces, are the addon operators declared, which
// certificate mode is in force, where is the chart.
//
// It is a read-only report: no cluster object is written, no GitOps commit
// is made.
func (s *Server) doctorV2(w http.ResponseWriter, r *http.Request) {
	p := s.cfg.Platform
	checks := make([]api.DoctorCheck, 0, 10)

	add := func(name string, ok bool, detail string) {
		checks = append(checks, api.DoctorCheck{Name: name, OK: ok, Detail: detail})
	}

	// The GitOps repository is where every manifest lands. Without it no
	// project can be compiled at all.
	if strings.TrimSpace(s.cfg.GitOpsURL) == "" {
		add("gitops", false, "no GitOps repository configured: projects cannot be compiled")
	} else {
		add("gitops", true, s.cfg.GitOpsURL)
	}

	// The stateless chart: a wrong reference makes every HelmRelease fail
	// after the compilation succeeded, which reads like a platform bug.
	if strings.TrimSpace(p.ChartRef) == "" {
		add("chart", false, "no stateless chart reference configured")
	} else {
		add("chart", true, fmt.Sprintf("%s (%s/%s)", p.ChartRef, p.ChartSourceKind, p.SourceName))
	}

	// The addon operators decide whether a manifest may declare a
	// database or a cache. An undeclared operator refuses the whole
	// project, not only the addon.
	if len(p.AddonOperators) == 0 {
		add("addon-operators", false, "no addon operator declared: a manifest with an addon is refused")
	} else {
		add("addon-operators", true, strings.Join(p.AddonOperators, ","))
	}

	// The certificate mode: cert-manager needs an issuer in the cluster,
	// Traefik ACME needs the resolver. Telling which one is in force
	// avoids debugging a Certificate that nobody will ever sign.
	if p.DisableCertManager {
		add("certificates", true, fmt.Sprintf("Traefik ACME (resolver %q): no cert-manager issuer required", p.TraefikCertResolver))
	} else {
		add("certificates", true, fmt.Sprintf("cert-manager (issuer %q): a ClusterIssuer of that name must exist", p.Issuer))
	}

	// The bookkeeping mode: a split platform keeps the workloads on
	// another cluster, so the control plane must not try to write the
	// registry secret and the values there.
	if s.cfg.SkipClusterBookkeeping {
		add("bookkeeping", true, "skipped: GitOps is the only writer (split platform)")
	} else {
		add("bookkeeping", true, "in-cluster: the control plane writes the pull secret and the values")
	}

	// The storage class of the volumes the addons and the applications
	// declare.
	if strings.TrimSpace(p.VolumeStorageClass) == "" {
		add("storage-class", true, "cluster default storage class")
	} else {
		add("storage-class", true, p.VolumeStorageClass)
	}

	// The secrets boundary. Without a project ID the operator has nothing
	// to sync and the application starts without its keys.
	if !p.InfisicalEnabled() {
		add("secrets", false, "Infisical is not configured: a service that declares secrets has none")
	} else {
		add("secrets", true, fmt.Sprintf("project %s, path %s, identity %s", p.InfisicalProjectID, p.InfisicalPath, p.InfisicalAuthRef))
	}

	// The exact permission that refused every project for forty minutes:
	// checkNamespaceOwnership reads a namespace before compiling, and a
	// ServiceAccount that cannot read it fails the project with a
	// "forbidden" that looks like a missing namespace.
	checks = append(checks, s.namespaceCheck(r.Context()))

	add("api-server-egress", true, apiServerEgressDetail(p.APIServerBlocks))

	writeJSON(w, http.StatusOK, api.Doctor{Version: api.Version, Checks: checks})
}

// namespaceCheck probes the cluster permission the compiler depends on. A
// nil client is not a failure: a platform that runs without a cluster
// client simply cannot adopt a namespace.
func (s *Server) namespaceCheck(ctx context.Context) api.DoctorCheck {
	if s.cfg.Kube == nil {
		return api.DoctorCheck{Name: "namespace-read", OK: true, Detail: "no cluster client: the ownership check is disabled"}
	}
	if _, err := s.cfg.Kube.CoreV1().Namespaces().Get(ctx, "default", metav1.GetOptions{}); err != nil {
		// NotFound means the permission worked and the object is absent:
		// only a real error (forbidden) is a broken preflight.
		if apierrors.IsNotFound(err) {
			return api.DoctorCheck{Name: "namespace-read", OK: true, Detail: "the control plane can read namespaces"}
		}
		return api.DoctorCheck{
			Name:   "namespace-read",
			OK:     false,
			Detail: "the control plane cannot read namespaces, so every project is refused: " + err.Error(),
		}
	}
	return api.DoctorCheck{Name: "namespace-read", OK: true, Detail: "the control plane can read namespaces"}
}

// apiServerEgressDetail says which API addresses the addon NetworkPolicies
// allow. An empty list is the cloud shape (the API is a pod); a list is the
// K3s shape (the API is a host process).
func apiServerEgressDetail(blocks []string) string {
	if len(blocks) == 0 {
		return "namespace rule only (the API is a pod in kube-system)"
	}
	return "extra CIDR blocks: " + strings.Join(blocks, ",")
}
