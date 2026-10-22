package control

import (
	"context"
	"log"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ensureBuilderCache creates the volume of the cache of the builder.
//
// The database of the vulnerabilities of Trivy is a cache: it is downloaded
// once and kept, so the build does not fill the disk of the node at every
// build.
//
// The volume is created by the platform and not by the chart: a claim of the
// chart waits for its first consumer, and nothing consumes it until the
// first build. The installation of the platform would then wait for a claim
// that never binds.
func (s *Server) ensureBuilderCache(ctx context.Context) error {
	if s.cfg.Kube == nil {
		return nil
	}
	namespace := s.cfg.BuilderNamespace
	if namespace == "" {
		namespace = "kuberpack-build"
	}
	name := s.cfg.BuilderCacheName
	if name == "" {
		name = "kuberpack-builder-cache"
	}
	size := s.cfg.BuilderCacheSize
	if size == "" {
		size = "2Gi"
	}
	quantity, err := resource.ParseQuantity(size)
	if err != nil {
		return err
	}
	claims := s.cfg.Kube.CoreV1().PersistentVolumeClaims(namespace)
	_, err = claims.Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		// The claim exists. Its size is not changed: a claim of a cache
		// does not grow, and a claim cannot shrink.
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	claim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{"app.kubernetes.io/name": "kuberpack-build"},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: quantity},
			},
		},
	}
	if _, err := claims.Create(ctx, claim, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return err
	}
	log.Printf("kuberpack: the cache of the builder is created (%s/%s, %s)", namespace, name, size)
	return nil
}
