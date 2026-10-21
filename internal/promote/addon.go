package promote

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// AddonSpec is what the manifest declares about an addon.
type AddonSpec struct {
	// Name is the name of the addon in the manifest.
	Name string
	// Project is the project the addon belongs to.
	Project string
	// Namespace is the namespace of the project.
	Namespace string
	// Database is the name of the database to create.
	Database string
	// Owner is the owner of the database.
	Owner string
	// Backup is the backup policy of the addon.
	Backup *Backup
	// Plan is the size of the addon, and Params what the manifest
	// overrides in that size.
	Plan   string
	Params map[string]string
	// Siblings are the other addons of the project, by name and by type.
	// A database that is backed up needs to know where the storage is.
	Siblings map[string]string
}

// AddonEnv is an environment variable an addon provides to the services that
// use it. The value stays in a Kubernetes Secret: Kuberpack only writes the
// reference.
type AddonEnv struct {
	// Name is the name of the environment variable.
	Name string
	// SecretName is the Secret that holds the value.
	SecretName string
	// SecretKey is the key of the value in the Secret.
	SecretKey string
	// Value is a literal value, when the addon does not need a Secret.
	Value string
}

// AddonResult is what an addon gives to the services that use it.
type AddonResult struct {
	// Env are the environment variables the addon provides.
	Env []AddonEnv
	// Kind is the kind of the addon (postgres, valkey). It labels the pods
	// of the addon, and the NetworkPolicy selects them by that label.
	Kind string
	// Port is the port the addon listens on.
	Port int
	// Secret is the name of the Secret of the addon, when it has one.
	Secret string
	// Service is the name the applications reach the addon by.
	Service string
}

// AddonEngine renders an infrastructure dependency of a project.
//
// An engine is a self-contained renderer: it writes the Kubernetes objects of
// the addon and declares what the services that use it need. Adding a type
// of addon is a new engine, not a change in the compiler.
type AddonEngine interface {
	// Name is the name of the engine in the manifest.
	Name() string
	// Render writes the objects of the addon in dir and returns what the
	// services that use it need.
	Render(dir string, spec AddonSpec, platform Platform) (AddonResult, error)
}

// addonEngines holds the available engines.
var addonEngines = map[string]AddonEngine{
	"cnpg":   cnpgEngine{},
	"valkey": valkeyEngine{},
	"rustfs": rustfsEngine{},
	"mongo":  mongoEngine{},
}

// AddonEngineNames lists the available engines.
func AddonEngineNames() []string {
	out := make([]string, 0, len(addonEngines))
	for name := range addonEngines {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// AddonEngineFor returns the engine of a manifest.
func AddonEngineFor(name string) (AddonEngine, bool) {
	engine, ok := addonEngines[strings.ToLower(strings.TrimSpace(name))]
	return engine, ok
}

// RegisterAddonEngine makes an engine available to the manifests. It is the
// only place where an addon type enters the product.
func RegisterAddonEngine(engine AddonEngine) {
	addonEngines[strings.ToLower(engine.Name())] = engine
}

// addonKustomization is the kustomization of an addon directory.
func addonKustomization(resources ...string) string {
	var b strings.Builder
	b.WriteString("apiVersion: kustomize.config.k8s.io/v1beta1\n")
	b.WriteString("kind: Kustomization\n")
	b.WriteString("\n")
	b.WriteString("resources:\n")
	for _, resource := range resources {
		b.WriteString("  - " + resource + "\n")
	}
	return b.String()
}

// addonFile is a file of an addon, written by its engine.
type addonFile struct {
	name    string
	content string
}

// writeAddonFiles writes the objects of an addon in its directory.
func writeAddonFiles(dir string, files ...addonFile) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, file := range files {
		path := filepath.Join(dir, file.name)
		if err := os.WriteFile(path, []byte(file.content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// envYAMLForAddon renders the environment variables an addon provides.
func envYAMLForAddon(envs []AddonEnv) string {
	var b strings.Builder
	for _, env := range envs {
		if strings.TrimSpace(env.Name) == "" {
			continue
		}
		b.WriteString("- name: " + env.Name + "\n")
		if env.Value != "" {
			b.WriteString("  value: " + strconv.Quote(env.Value) + "\n")
			continue
		}
		b.WriteString("  valueFrom:\n")
		b.WriteString("    secretKeyRef:\n")
		b.WriteString("      name: " + env.SecretName + "\n")
		b.WriteString("      key: " + env.SecretKey + "\n")
	}
	return b.String()
}

// addonNetworkPolicy renders the network isolation of the pods of an addon.
//
// The pods of an addon are reachable by the applications of their project
// and by nothing else. Without this policy, a database is reachable by
// every pod of the cluster.
func addonNetworkPolicy(name, namespace, kind, addonName, project string, ports []int, operatorNamespace string) string {
	var b strings.Builder
	b.WriteString("# Generated by Kuberpack from the project manifest.\n")
	b.WriteString("# The pods of the addon are reachable by the applications of the project,\n")
	b.WriteString("# and by nothing else.\n")
	b.WriteString("apiVersion: networking.k8s.io/v1\n")
	b.WriteString("kind: NetworkPolicy\n")
	b.WriteString("metadata:\n")
	b.WriteString("  name: " + name + "\n")
	b.WriteString("  namespace: " + namespace + "\n")
	b.WriteString("spec:\n")
	b.WriteString("  podSelector:\n")
	b.WriteString("    matchLabels:\n")
	b.WriteString("      kuberpack.io/addon: " + kind + "\n")
	b.WriteString("      kuberpack.io/addon-name: " + addonName + "\n")
	b.WriteString("      kuberpack.io/for: " + project + "\n")
	b.WriteString("  policyTypes: [Ingress]\n")
	b.WriteString("  ingress:\n")
	b.WriteString("    - from:\n")
	b.WriteString("        - podSelector:\n")
	b.WriteString("            matchLabels:\n")
	b.WriteString("              app.kubernetes.io/part-of: kuberpack\n")
	if operatorNamespace != "" {
		b.WriteString("        - namespaceSelector:\n")
		b.WriteString("            matchLabels:\n")
		b.WriteString("              kubernetes.io/metadata.name: " + operatorNamespace + "\n")
	}
	b.WriteString("      ports:\n")
	for _, port := range ports {
		b.WriteString("        - { protocol: TCP, port: " + strconv.Itoa(port) + " }\n")
	}
	return b.String()
}

// addonBootstrap renders the Job that creates the credentials of an addon,
// and the Role that lets it do so.
//
// The password is born in the cluster: it never passes through Kuberpack,
// and it is never in the repository. The Job is idempotent: the secret it
// created is kept, so the password does not change under the feet of the
// applications.
func addonBootstrap(name, namespace, image, secret string, keys []string) (job string, rbac string) {
	script := `set -eu
SA=/var/run/secrets/kubernetes.io/serviceaccount
API=https://${KUBERNETES_SERVICE_HOST}:${KUBERNETES_SERVICE_PORT_HTTPS:-443}
TOKEN=$(cat $SA/token)
if curl -sSf --cacert $SA/ca.crt -H "Authorization: Bearer $TOKEN" "$API/api/v1/namespaces/$NAMESPACE/secrets/$SECRET" >/dev/null 2>&1; then
  echo "the secret already exists: the credentials are kept"
  exit 0
fi
VALUE=$(head -c 48 /dev/urandom | base64 | tr -d '=+/\n' | cut -c1-40)
printf '{"apiVersion":"v1","kind":"Secret","metadata":{"name":"%s"},"stringData":{' "$SECRET" > /tmp/secret.json
`
	for i, key := range keys {
		sep := ""
		if i > 0 {
			sep = ","
		}
		script += "printf '" + sep + "\"" + key + "\":\"$VALUE\"' >> /tmp/secret.json\n"
	}
	script += `printf '}}' >> /tmp/secret.json
curl -sSf --cacert $SA/ca.crt -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" --data-binary @/tmp/secret.json "$API/api/v1/namespaces/$NAMESPACE/secrets"
echo "the credentials are born"
`

	var b strings.Builder
	b.WriteString("# Generated by Kuberpack from the project manifest.\n")
	b.WriteString("# The credentials of the addon are born here, in the cluster.\n")
	b.WriteString("apiVersion: batch/v1\n")
	b.WriteString("kind: Job\n")
	b.WriteString("metadata:\n")
	b.WriteString("  name: " + name + "-bootstrap\n")
	b.WriteString("  namespace: " + namespace + "\n")
	b.WriteString("spec:\n")
	b.WriteString("  backoffLimit: 3\n")
	b.WriteString("  template:\n")
	b.WriteString("    metadata:\n")
	b.WriteString("      labels:\n")
	b.WriteString("        kuberpack.io/addon: " + name + "\n")
	b.WriteString("    spec:\n")
	b.WriteString("      restartPolicy: Never\n")
	b.WriteString("      serviceAccountName: " + name + "-bootstrap\n")
	b.WriteString("      containers:\n")
	b.WriteString("        - name: bootstrap\n")
	b.WriteString("          image: " + image + "\n")
	b.WriteString("          env:\n")
	b.WriteString("            - name: NAMESPACE\n")
	b.WriteString("              value: " + namespace + "\n")
	b.WriteString("            - name: SECRET\n")
	b.WriteString("              value: " + secret + "\n")
	b.WriteString("          command: [\"sh\", \"-c\"]\n")
	b.WriteString("          args:\n")
	b.WriteString("            - |\n")
	for _, line := range strings.Split(strings.TrimRight(script, "\n"), "\n") {
		b.WriteString("              " + line + "\n")
	}
	job = b.String()

	var r strings.Builder
	r.WriteString("# Generated by Kuberpack from the project manifest.\n")
	r.WriteString("# The Job that creates the credentials of the addon writes one secret,\n")
	r.WriteString("# in the namespace of the addon. The credentials stay in the cluster.\n")
	r.WriteString("apiVersion: v1\n")
	r.WriteString("kind: ServiceAccount\n")
	r.WriteString("metadata:\n")
	r.WriteString("  name: " + name + "-bootstrap\n")
	r.WriteString("  namespace: " + namespace + "\n")
	r.WriteString("automountServiceAccountToken: true\n")
	r.WriteString("---\n")
	r.WriteString("apiVersion: rbac.authorization.k8s.io/v1\n")
	r.WriteString("kind: Role\n")
	r.WriteString("metadata:\n")
	r.WriteString("  name: " + name + "-bootstrap\n")
	r.WriteString("  namespace: " + namespace + "\n")
	r.WriteString("rules:\n")
	r.WriteString("  - apiGroups: [\"\"]\n")
	r.WriteString("    resources: [secrets]\n")
	r.WriteString("    resourceNames: [" + secret + "]\n")
	r.WriteString("    verbs: [get]\n")
	r.WriteString("  - apiGroups: [\"\"]\n")
	r.WriteString("    resources: [secrets]\n")
	r.WriteString("    verbs: [create]\n")
	r.WriteString("---\n")
	r.WriteString("apiVersion: rbac.authorization.k8s.io/v1\n")
	r.WriteString("kind: RoleBinding\n")
	r.WriteString("metadata:\n")
	r.WriteString("  name: " + name + "-bootstrap\n")
	r.WriteString("  namespace: " + namespace + "\n")
	r.WriteString("subjects:\n")
	r.WriteString("  - kind: ServiceAccount\n")
	r.WriteString("    name: " + name + "-bootstrap\n")
	r.WriteString("    namespace: " + namespace + "\n")
	r.WriteString("roleRef:\n")
	r.WriteString("  apiGroup: rbac.authorization.k8s.io\n")
	r.WriteString("  kind: Role\n")
	r.WriteString("  name: " + name + "-bootstrap\n")
	return job, r.String()
}
