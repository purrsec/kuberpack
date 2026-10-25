package platformcli

import (
	"context"
	"strings"

	"gopkg.in/yaml.v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// durableInfrastructure hands the non-secret bootstrap resources to Flux. The
// registry credentials, encryption keys and bootstrap tokens remain in Secrets.
func (i *installation) durableInfrastructure(ctx context.Context) ([]byte, error) {
	var documents []map[string]any
	if i.c.Infisical {
		for _, resource := range []struct{ plural, name string }{{"infisicalconnections", "infisical-connection"}, {"infisicalauths", infisicalAuthName}} {
			obj, err := i.dyn.Resource(schema.GroupVersionResource{Group: "secrets.infisical.com", Version: "v1beta1", Resource: resource.plural}).Namespace(infisicalNamespace).Get(ctx, resource.name, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}
			doc := object(obj.GetAPIVersion(), obj.GetKind(), obj.GetNamespace(), obj.GetName(), obj.Object["spec"].(map[string]any))
			documents = append(documents, doc.Object)
		}
		documents = append(documents, map[string]any{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": map[string]any{"name": "infisical-service-account", "namespace": infisicalNamespace}}, map[string]any{
			"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding", "metadata": map[string]any{"name": "infisical-token-reviewer-binding"},
			"roleRef": map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": "system:auth-delegator"}, "subjects": []any{map[string]any{"kind": "ServiceAccount", "name": "infisical-service-account", "namespace": infisicalNamespace}},
		})
	}
	if i.c.TLS {
		issuer, err := i.dyn.Resource(schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "clusterissuers"}).Get(ctx, i.c.Issuer, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		if issuer.GetLabels()[installOwner] == i.c.Cluster {
			doc := object(issuer.GetAPIVersion(), issuer.GetKind(), "", issuer.GetName(), issuer.Object["spec"].(map[string]any))
			// Flux adds inventory labels after handover. Persist only our
			// ownership label so observed controller metadata cannot cause
			// a new GitOps commit on the next installation run.
			doc.SetLabels(map[string]string{installOwner: i.c.Cluster})
			documents = append(documents, doc.Object)
		}
	}
	var out strings.Builder
	for _, doc := range documents {
		data, err := yaml.Marshal(doc)
		if err != nil {
			return nil, err
		}
		out.Write(data)
		out.WriteString("---\n")
	}
	return []byte(out.String()), nil
}
