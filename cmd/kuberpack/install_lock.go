package main

import (
	"context"
	"fmt"
	"time"

	coordv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// lock prevents two installers from bootstrapping the same release concurrently.
// An abandoned lock expires; successful and interrupted runs release it.
func (i *installation) lock(parent context.Context) (context.Context, func(), error) {
	if err := ensureNamespace(parent, i.kube, i.c.Namespace); err != nil {
		return nil, nil, err
	}
	client := i.kube.CoordinationV1().Leases(i.c.Namespace)
	holder := "install-" + randomSecret(12)
	duration := int32(90)
	now := metav1.NewMicroTime(time.Now())
	lease, err := client.Get(parent, "kuberpack-install", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		lease = &coordv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: "kuberpack-install"}, Spec: coordv1.LeaseSpec{HolderIdentity: &holder, LeaseDurationSeconds: &duration, RenewTime: &now}}
		lease, err = client.Create(parent, lease, metav1.CreateOptions{})
	} else if err == nil {
		if lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity != "" && lease.Spec.RenewTime != nil && lease.Spec.LeaseDurationSeconds != nil && lease.Spec.RenewTime.Add(time.Duration(*lease.Spec.LeaseDurationSeconds)*time.Second).After(time.Now()) {
			return nil, nil, fmt.Errorf("another installation holds the cluster lock; wait for it to finish or for its 90-second lease to expire")
		}
		lease.Spec = coordv1.LeaseSpec{HolderIdentity: &holder, LeaseDurationSeconds: &duration, RenewTime: &now}
		lease, err = client.Update(parent, lease, metav1.UpdateOptions{})
	}
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				current, err := client.Get(ctx, "kuberpack-install", metav1.GetOptions{})
				if err != nil || current.Spec.HolderIdentity == nil || *current.Spec.HolderIdentity != holder {
					cancel()
					return
				}
				now := metav1.NewMicroTime(time.Now())
				current.Spec.RenewTime = &now
				if _, err := client.Update(ctx, current, metav1.UpdateOptions{}); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, func() {
		cancel()
		<-done
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		current, err := client.Get(cleanup, "kuberpack-install", metav1.GetOptions{})
		if err == nil && current.Spec.HolderIdentity != nil && *current.Spec.HolderIdentity == holder {
			empty := ""
			current.Spec.HolderIdentity = &empty
			_, _ = client.Update(cleanup, current, metav1.UpdateOptions{})
		}
	}, nil
}
