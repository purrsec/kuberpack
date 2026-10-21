package control

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"git.host.bzh/pepe/kuberpack/internal/git"
	"git.host.bzh/pepe/kuberpack/internal/hmacsig"
	"git.host.bzh/pepe/kuberpack/internal/promote"
	"git.host.bzh/pepe/kuberpack/internal/release"
	"git.host.bzh/pepe/kuberpack/internal/store"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// reconcileApps makes the operational state of a project match its manifest.
//
// The manifest is the source of truth: every service that declares a
// repository gets an app record, an HMAC secret and a Forgejo webhook, so the
// push of that repository triggers a build. A service that left the manifest
// loses its app, its webhook and its secret: nothing is left dangling.
//
// The reconciliation is idempotent: reconciling twice does not duplicate a
// webhook, rotate a secret or start a second build.
func (s *Server) reconcileApps(ctx context.Context, project string, apps []promote.ProjectApp) error {
	existing, err := s.cfg.Store.AppsByProject(ctx, project)
	if err != nil {
		return err
	}
	previous := make(map[string]store.App, len(existing))
	for _, app := range existing {
		previous[app.Name] = app
	}

	var errs []error
	declared := make(map[string]bool, len(apps))
	for _, desc := range apps {
		declared[desc.Name] = true
		if err := s.reconcileApp(ctx, project, desc, previous[desc.Name]); err != nil {
			errs = append(errs, err)
		}
	}
	for _, app := range existing {
		if declared[app.Name] {
			continue
		}
		if err := s.removeApp(ctx, app); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// reconcileApp aligns one app with its declaration.
func (s *Server) reconcileApp(ctx context.Context, project string, desc promote.ProjectApp, previous store.App) error {
	record := appFromManifest(project, desc)
	app, created, err := s.cfg.Store.UpsertApp(ctx, record)
	if err != nil {
		return fmt.Errorf("app %s: %w", desc.Name, err)
	}

	// The secret is the key of the webhook signature: it is created once and
	// kept, so a reconciliation does not invalidate the pending deliveries.
	secret, err := s.cfg.Secrets.Get(app.ID)
	if err != nil || strings.TrimSpace(secret) == "" {
		secret, err = hmacsig.NewSecret()
		if err != nil {
			return fmt.Errorf("app %s: %w", desc.Name, err)
		}
		if err := s.cfg.Secrets.Put(app.ID, secret); err != nil {
			return fmt.Errorf("app %s: %w", desc.Name, err)
		}
	}

	// The application pulls the image of the platform's registry: the secret
	// that holds the credentials lives in the cluster, in the namespace of
	// the application. It is never written in the GitOps repository.
	if err := s.ensurePullSecret(ctx, desc.Namespace); err != nil {
		log.Printf("kuberpack app %s: the secret of the registry: %v", desc.Name, err)
	}

	if s.cfg.WebhookURL != "" && strings.TrimSpace(app.ForgejoRepository) != "" {
		provider, owner, repo, err := s.appForge(app)
		if err != nil {
			return fmt.Errorf("app %s: %w", desc.Name, err)
		}
		hook, err := provider.EnsureHook(ctx, owner, repo, s.cfg.WebhookURL, secret)
		if err != nil {
			// The repository of a third party application is not ours to
			// write in: the webhook cannot be posed. The application is
			// still deployed, and a push on the manifest rebuilds it. This
			// is a warning, not a failure.
			log.Printf("kuberpack app %s: webhook not configured on %s: %v", desc.Name, app.ForgejoRepository, err)
		} else {
			if err := s.cfg.Store.SetWebhookID(ctx, app.ID, hook.ID); err != nil {
				return fmt.Errorf("app %s: %w", desc.Name, err)
			}
			app.WebhookID = hook.ID
		}
	}

	// A new app is built right away. An app whose build parameters changed
	// is built again: the manifest asked for something else.
	// An application that runs a published image is not built.
	if strings.TrimSpace(app.ForgejoRepository) != "" && (created || buildParametersChanged(previous, app)) {
		if err := s.queueFirstBuild(ctx, app); err != nil {
			return fmt.Errorf("app %s: %w", desc.Name, err)
		}
	}
	return nil
}

// ensurePullSecret makes sure the application can pull the images of the
// platform's registry. The secret is written in the cluster: a credential
// does not belong in a Git repository.
func (s *Server) ensurePullSecret(ctx context.Context, namespace string) error {
	if s.cfg.Kube == nil {
		return nil
	}
	name := strings.TrimSpace(s.cfg.Platform.ImagePullSecret)
	host := release.RegistryHost(s.cfg.RegistryURL)
	if name == "" || host == "" || namespace == "" {
		return nil
	}
	user := strings.TrimSpace(s.cfg.RegistryUser)
	if user == "" {
		user = "token"
	}
	auth := base64.StdEncoding.EncodeToString([]byte(user + ":" + s.cfg.Token))
	payload, err := json.Marshal(map[string]any{
		"auths": map[string]any{
			host: map[string]string{
				"username": user,
				"password": s.cfg.Token,
				"auth":     auth,
			},
		},
	})
	if err != nil {
		return err
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: payload},
	}
	secrets := s.cfg.Kube.CoreV1().Secrets(namespace)
	if _, err := secrets.Create(ctx, secret, metav1.CreateOptions{}); err == nil {
		return nil
	} else if !apierrors.IsAlreadyExists(err) {
		return err
	}
	_, err = secrets.Update(ctx, secret, metav1.UpdateOptions{})
	return err
}

// removeApp drops everything an app owns: the webhook, the secret and the
// record.
func (s *Server) removeApp(ctx context.Context, app store.App) error {
	if app.WebhookID > 0 {
		if provider, owner, repo, err := s.appForge(app); err == nil {
			_ = provider
			_ = s.cfg.Provider.DeleteHook(ctx, owner, repo, app.WebhookID)
		}
	}
	_ = s.cfg.Secrets.Delete(app.ID)
	return s.cfg.Store.DeleteApp(ctx, app.ID)
}

// appValuesPath is where the digest of an app is written, relative to the
// root of the GitOps repository. An app of a project has its own path; an app
// outside a project falls back to the platform default.
func appValuesPath(platform promote.Platform, app store.App) string {
	if path := strings.TrimSpace(app.ValuesPath); path != "" {
		return path
	}
	return platform.ValuesPath(app.Name)
}

// appFromManifest projects a declared service into the app record. The record
// is a cache of the manifest: the manifest stays the source of truth.
func appFromManifest(project string, desc promote.ProjectApp) store.App {
	internet := true
	if desc.Internet != nil {
		internet = *desc.Internet
	}
	return store.App{
		Name:              desc.Name,
		Project:           project,
		ValuesPath:        desc.ValuesPath,
		ForgejoRepository: desc.Repository,
		ProductionBranch:  desc.Branch,
		Strategy:          desc.Strategy,
		Autodeploy:        true,
		Hostname:          desc.Hostname,
		Port:              desc.Port,
		Healthcheck:       desc.Healthcheck,
		StartCommand:      desc.StartCommand,
		RootDirectory:     desc.RootDirectory,
		BuildCommand:      desc.BuildCommand,
		Internet:          internet,
		Peers:             desc.Peers,
		Secrets:           desc.Secrets,
		SecretEnv:         desc.SecretEnv,
	}
}

// buildParametersChanged reports whether the manifest asked for a different
// build than the one recorded: a new repository, a new branch, a new strategy
// or new build overrides.
func buildParametersChanged(previous, current store.App) bool {
	if previous.ID == 0 {
		return false
	}
	return previous.ForgejoRepository != current.ForgejoRepository ||
		previous.ProductionBranch != current.ProductionBranch ||
		previous.Strategy != current.Strategy ||
		previous.StartCommand != current.StartCommand ||
		previous.RootDirectory != current.RootDirectory ||
		previous.BuildCommand != current.BuildCommand
}

// queueFirstBuild starts the first build of an app. A repository that cannot
// be reached is not an error: the push of that repository will trigger the
// build later.
func (s *Server) queueFirstBuild(ctx context.Context, app store.App) error {
	provider, owner, repo, err := s.appForge(app)
	if err != nil {
		return err
	}
	sha, err := provider.BranchSHA(ctx, owner, repo, app.ProductionBranch)
	if err != nil {
		// The branch cannot be read: the build waits for the next push.
		// It is said out loud: a build that never starts must not be
		// silent.
		log.Printf("kuberpack app %s: branch %s of %s not read: %v", app.Name, app.ProductionBranch, app.ForgejoRepository, err)
		return nil
	}
	if strings.TrimSpace(sha) == "" {
		log.Printf("kuberpack app %s: branch %s of %s is empty", app.Name, app.ProductionBranch, app.ForgejoRepository)
		return nil
	}
	del := "register-" + app.Name + "-" + sha + "-" + deliveryNonce()
	if err := s.cfg.Store.InsertDelivery(ctx, store.Delivery{
		ID:        del,
		AppID:     app.ID,
		EventType: "register",
		CommitSHA: sha,
		Status:    "queued",
	}); err != nil && !errors.Is(err, store.ErrDuplicate) {
		return err
	}
	s.publishCommitStatus(ctx, provider, owner, repo, sha, git.StatusPending, "Building", app, false)
	s.kick()
	return nil
}

// isThirdParty reports whether the code of an application lives in a
// repository that is not ours. The platform never writes in it: no
// webhook, no commit status.
func isThirdParty(app store.App) bool {
	repository := strings.TrimSpace(app.ForgejoRepository)
	return strings.HasPrefix(repository, "http://") || strings.HasPrefix(repository, "https://")
}
