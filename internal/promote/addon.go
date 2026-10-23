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
	// Version is the version of the addon the manifest asks for: the
	// major version of the database, for a database.
	Version string
	// Path is where the addon mounts its volume, when it has one.
	Path string
	// Siblings are the other addons of the project, by name and by type.
	// A database that is backed up needs to know where the storage is.
	Siblings map[string]string
	// Clients are the exact services and addons whose manifest declares a
	// dependency on this addon. An addon is not shared with the whole project.
	ServiceClients []string
	AddonClients   []string
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
	// Mounts are the volumes the addon mounts in the applications that
	// use it. A database that is a file is a volume: there is nothing to
	// run, and nothing to reach.
	Mounts []AddonMount
}

// AddonMount is a volume an addon mounts in the applications that use it.
type AddonMount struct {
	// Name is the name of the volume in the application.
	Name string
	// Path is where the volume is mounted.
	Path string
	// Claim is the name of the PersistentVolumeClaim the volume comes
	// from. It is empty when the volume is a temporary directory.
	Claim string
	// Size is the size of the disk.
	Size string
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
	"volume": volumeEngine{},
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

// addonEgressPeer est un ensemble de pods qu'un addon, ou l'un de ses
// helpers, peut joindre, avec les ports TCP qu'il peut utiliser. Les labels
// sont ceux que Kuberpack écrit sur ses propres pods : une règle ne nomme
// jamais le pod d'un autre projet.
type addonEgressPeer struct {
	// Kind est le label kuberpack.io/addon du pair. Vide : n'importe quel
	// type d'addon du projet.
	Kind string
	// AddonName est le label kuberpack.io/addon-name du pair.
	AddonName string
	// Ports sont les ports TCP sur lesquels le pair écoute.
	Ports []int
}

// addonEgress décrit les flux sortants d'un addon, ou de l'un de ses
// helpers.
//
// La policy est additive : un champ ajouté ouvre un flux, il n'en ferme
// aucun. Tout ce qui n'est pas décrit ici est refusé dès qu'un pod est
// sélectionné par une policy qui déclare Egress.
type addonEgress struct {
	// KubernetesAPI autorise l'API du cluster, dans le namespace
	// kube-system.
	KubernetesAPI bool
	// OperatorNamespace est le namespace de l'opérateur du moteur. Il est
	// vide pour un moteur sans opérateur : aucune règle n'est écrite,
	// donc la policy ne nomme jamais un namespace qui n'existe pas.
	OperatorNamespace string
	// OperatorPorts sont les ports TCP de l'opérateur.
	OperatorPorts []int
	// ReplicaPorts sont les ports TCP que les instances du même addon
	// utilisent entre elles.
	ReplicaPorts []int
	// Peers sont les autres addons que les pods peuvent joindre : le
	// stockage des sauvegardes, ou la base qu'un dump lit.
	Peers []addonEgressPeer
}

// sortedTCPPorts rend les ports dans un ordre déterministe, sans doublon :
// la policy est un fichier généré, deux compilations du même manifeste
// doivent écrire les mêmes octets.
func sortedTCPPorts(ports []int) []int {
	seen := make(map[int]bool, len(ports))
	out := make([]int, 0, len(ports))
	for _, port := range ports {
		if port <= 0 || seen[port] {
			continue
		}
		seen[port] = true
		out = append(out, port)
	}
	sort.Ints(out)
	return out
}

// writeTCPPorts écrit les ports d'une règle d'egress. Il n'écrit rien sans
// port : une règle sans port autoriserait tous les ports du pair.
func writeTCPPorts(b *strings.Builder, ports []int) bool {
	ports = sortedTCPPorts(ports)
	if len(ports) == 0 {
		return false
	}
	b.WriteString("      ports:\n")
	for _, port := range ports {
		b.WriteString("        - { protocol: TCP, port: " + strconv.Itoa(port) + " }\n")
	}
	return true
}

// writeAddonDNSEgress écrit la règle qui résout les noms. Elle est toujours
// là : un pod qui ne résout pas un Service ne joint ni sa base, ni son
// stockage, ni l'API.
func writeAddonDNSEgress(b *strings.Builder) {
	b.WriteString("    - to:\n")
	b.WriteString("        - namespaceSelector:\n")
	b.WriteString("            matchLabels:\n")
	b.WriteString("              kubernetes.io/metadata.name: kube-system\n")
	b.WriteString("          podSelector:\n")
	b.WriteString("            matchLabels:\n")
	b.WriteString("              k8s-app: kube-dns\n")
	b.WriteString("      ports:\n")
	b.WriteString("        - { protocol: UDP, port: 53 }\n")
	b.WriteString("        - { protocol: TCP, port: 53 }\n")
}

// writeAddonAPIEgress écrit la règle qui joint l'API. L'API n'est pas
// toujours un pod : la règle nomme le namespace kube-system, qui existe sur
// chaque cluster, et jamais un namespace de projet.
func writeAddonAPIEgress(b *strings.Builder) {
	b.WriteString("    - to:\n")
	b.WriteString("        - namespaceSelector:\n")
	b.WriteString("            matchLabels:\n")
	b.WriteString("              kubernetes.io/metadata.name: kube-system\n")
	b.WriteString("      ports:\n")
	b.WriteString("        - { protocol: TCP, port: 443 }\n")
	b.WriteString("        - { protocol: TCP, port: 6443 }\n")
}

// writeAddonOperatorEgress écrit la règle qui joint l'opérateur du moteur.
// Elle n'est écrite que pour un moteur qui a un opérateur : une policy ne
// doit pas nommer un namespace qui n'existe pas.
func writeAddonOperatorEgress(b *strings.Builder, namespace string, ports []int) {
	ports = sortedTCPPorts(ports)
	if len(ports) == 0 {
		return
	}
	b.WriteString("    - to:\n")
	b.WriteString("        - namespaceSelector:\n")
	b.WriteString("            matchLabels:\n")
	b.WriteString("              kubernetes.io/metadata.name: " + namespace + "\n")
	writeTCPPorts(b, ports)
}

// writeAddonPeerEgress écrit une règle vers des pods du même projet,
// sélectionnés par leurs labels d'addon. Une règle sans port est ignorée :
// elle autoriserait tous les ports du pair.
func writeAddonPeerEgress(b *strings.Builder, peer addonEgressPeer, project string) {
	ports := sortedTCPPorts(peer.Ports)
	if len(ports) == 0 || strings.TrimSpace(peer.AddonName) == "" {
		return
	}
	b.WriteString("    - to:\n")
	b.WriteString("        - podSelector:\n")
	b.WriteString("            matchLabels:\n")
	if strings.TrimSpace(peer.Kind) != "" {
		b.WriteString("              kuberpack.io/addon: " + peer.Kind + "\n")
	}
	b.WriteString("              kuberpack.io/addon-name: " + peer.AddonName + "\n")
	b.WriteString("              kuberpack.io/for: " + project + "\n")
	writeTCPPorts(b, ports)
}

// addonNetworkPolicy renders the network isolation of the pods of an addon.
//
// The pods of an addon are reachable by the applications of their project
// and by nothing else. Without this policy, a database is reachable by
// every pod of the cluster.
//
// La même policy ferme l'egress des pods : ils joignent le DNS, l'API et
// l'opérateur quand le moteur en a besoin, les autres instances du même
// addon, et le stockage nommé par backup.s3. Une règle qui n'est pas écrite
// est un flux qui est refusé.
func addonNetworkPolicy(name, namespace, kind, addonName, project string, ports []int, egress addonEgress, clients AddonSpec) string {
	var b strings.Builder
	b.WriteString("# Generated by Kuberpack from the project manifest.\n")
	b.WriteString("# The pods of the addon are reachable by the applications of the project,\n")
	b.WriteString("# and by nothing else. They reach only what the engine needs.\n")
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
	b.WriteString("  policyTypes: [Ingress, Egress]\n")
	b.WriteString("  ingress:\n")
	b.WriteString("    - from:\n")
	for _, client := range clients.ServiceClients {
		b.WriteString("        - podSelector:\n            matchLabels:\n")
		b.WriteString("              app.kubernetes.io/name: " + client + "\n")
		b.WriteString("              app.kubernetes.io/part-of: kuberpack\n")
		b.WriteString("              kuberpack.io/project: " + project + "\n")
	}
	// A replica may reach its own cluster; a database may reach only the
	// storage explicitly named by backup.s3. Bucket bootstrap/dump helpers
	// carry the same dependency identity as their database.
	for _, client := range append([]string{addonName}, clients.AddonClients...) {
		b.WriteString("        - podSelector:\n            matchLabels:\n")
		b.WriteString("              kuberpack.io/addon-name: " + client + "\n")
		b.WriteString("              kuberpack.io/for: " + project + "\n")
	}
	if strings.TrimSpace(egress.OperatorNamespace) != "" {
		b.WriteString("        - namespaceSelector:\n")
		b.WriteString("            matchLabels:\n")
		b.WriteString("              kubernetes.io/metadata.name: " + egress.OperatorNamespace + "\n")
	}
	b.WriteString("      ports:\n")
	for _, port := range ports {
		b.WriteString("        - { protocol: TCP, port: " + strconv.Itoa(port) + " }\n")
	}
	b.WriteString("  egress:\n")
	writeAddonDNSEgress(&b)
	if egress.KubernetesAPI {
		writeAddonAPIEgress(&b)
	}
	writeAddonOperatorEgress(&b, egress.OperatorNamespace, egress.OperatorPorts)
	writeAddonPeerEgress(&b, addonEgressPeer{AddonName: addonName, Ports: egress.ReplicaPorts}, project)
	for _, peer := range egress.Peers {
		writeAddonPeerEgress(&b, peer, project)
	}
	return b.String()
}

// addonHelperNetworkPolicy écrit l'egress d'un Job helper d'un addon (le
// bootstrap de ses credentials, le dump de ses données, la création de son
// bucket).
//
// Un helper n'est pas une instance de l'addon : il ne joint que l'API, la
// base ou le stockage qu'il utilise. Son ingress est déjà refusé par le
// default deny du namespace.
func addonHelperNetworkPolicy(name, namespace, project, addonName, helper string, api bool, peers []addonEgressPeer) string {
	var b strings.Builder
	b.WriteString("# Generated by Kuberpack from the project manifest.\n")
	b.WriteString("# A helper Job of the addon reaches only what it uses.\n")
	b.WriteString("apiVersion: networking.k8s.io/v1\n")
	b.WriteString("kind: NetworkPolicy\n")
	b.WriteString("metadata:\n")
	b.WriteString("  name: " + name + "-" + helper + "\n")
	b.WriteString("  namespace: " + namespace + "\n")
	b.WriteString("spec:\n")
	b.WriteString("  podSelector:\n")
	b.WriteString("    matchLabels:\n")
	b.WriteString("      kuberpack.io/helper: " + helper + "\n")
	b.WriteString("      kuberpack.io/addon-name: " + addonName + "\n")
	b.WriteString("      kuberpack.io/for: " + project + "\n")
	b.WriteString("  policyTypes: [Egress]\n")
	b.WriteString("  egress:\n")
	writeAddonDNSEgress(&b)
	if api {
		writeAddonAPIEgress(&b)
	}
	for _, peer := range peers {
		writeAddonPeerEgress(&b, peer, project)
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
//
// Le pod porte les labels d'un helper, pas ceux d'un addon : sa policy
// d'egress est celle d'un helper, pas celle d'une base.
func addonBootstrap(name, namespace, project, addonName, image, secret string, keys []string) (job string, rbac string) {
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
	b.WriteString("        kuberpack.io/helper: bootstrap\n")
	b.WriteString("        kuberpack.io/addon-name: " + addonName + "\n")
	b.WriteString("        kuberpack.io/for: " + project + "\n")
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
