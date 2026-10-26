package platformcli

import (
	"context"
	"fmt"
	"net/url"
	"reflect"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// forgeBaseURL returns the URL of the forge that hosts the repository. The
// repository of the user lives on the forge of its URL.
func forgeBaseURL(repositoryURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(repositoryURL))
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	scheme := parsed.Scheme
	if scheme == "" {
		scheme = "https"
	}
	return scheme + "://" + parsed.Host
}

// forgeUser returns the user of the repository: the first segment of the
// path.
func forgeUser(repositoryURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(repositoryURL))
	if err != nil {
		return ""
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) == 0 || segments[0] == "" {
		return ""
	}
	return segments[0]
}

// registryOf returns the OCI registry the images are pushed to, and the
// namespace of the images in it. GitHub keeps them in ghcr.io; a self
// hosted forge keeps them where it is.
func registryOf(forgeURL, user string) (registryURL, registryUser string) {
	if strings.TrimSpace(forgeURL) == "" {
		return "", ""
	}
	if strings.Contains(forgeURL, "github.com") {
		return "ghcr.io", user
	}
	return forgeURL, user
}

// ensureNamespace creates a namespace when it does not exist.
func ensureNamespace(ctx context.Context, client kubernetes.Interface, name string) error {
	_, err := client.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	_, err = client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// ensureSecret creates a secret, or replaces its content.
//
// The type of a Secret is immutable. A Secret that is already there keeps
// the type it has: a pull secret is a docker config, and Kubernetes refuses
// the change of type. The type of a Secret that is created is deduced from
// its data, for the same reason.
func ensureSecret(ctx context.Context, client kubernetes.Interface, namespace, name string, data map[string][]byte) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Type:       secretTypeFor(data),
		Data:       data,
	}
	_, err := client.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return err
	}
	existing, err := client.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	secret.Type = existing.Type
	// Preserve unrelated keys and avoid writing a new resourceVersion on a
	// no-op reinstall. Credential generation is handled separately.
	merged := map[string][]byte{}
	for key, value := range existing.Data {
		merged[key] = value
	}
	for key, value := range data {
		merged[key] = value
	}
	if reflect.DeepEqual(existing.Data, merged) {
		return nil
	}
	secret.Data = merged
	secret.Labels = existing.Labels
	secret.Annotations = existing.Annotations
	secret.ResourceVersion = existing.ResourceVersion
	_, err = client.CoreV1().Secrets(namespace).Update(ctx, secret, metav1.UpdateOptions{})
	return err
}

// secretTypeFor returns the type a Secret with this data must have.
func secretTypeFor(data map[string][]byte) corev1.SecretType {
	if _, ok := data[corev1.DockerConfigJsonKey]; ok {
		return corev1.SecretTypeDockerConfigJson
	}
	return corev1.SecretTypeOpaque
}

// dockerConfigJSON is the content of a pull secret.
func dockerConfigJSON(host, user, token string) string {
	auth := base64Encode(user + ":" + token)
	return fmt.Sprintf(`{"auths":{%q:{"username":%q,"password":%q,"auth":%q}}}`,
		host, user, token, auth)
}
