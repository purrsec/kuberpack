package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/api"
	"git.host.bzh/pepe/kuberpack/internal/promote"
	"git.host.bzh/pepe/kuberpack/internal/spec"
	"git.host.bzh/pepe/kuberpack/internal/store"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type savedDeployment struct {
	api.Deployment
	ApprovedPlan api.Plan `json:"approved_plan"`
	TokenID      int64    `json:"token_id"`
}

func (s *Server) executeV2(w http.ResponseWriter, r *http.Request) {
	p, project, ok := s.projectAccess(w, r)
	if !ok {
		return
	}
	var body api.DeploymentRequest
	if err := readJSON(r, &body); err != nil {
		apiError(w, 400, "invalid_request", err.Error())
		return
	}
	if body.RequestID == "" || len(body.RequestID) > 200 || strings.ContainsAny(body.RequestID, "\r\n\x00") {
		apiError(w, 400, "request_id_required", "a request_id of at most 200 characters is required")
		return
	}
	s.deploymentMu.Lock()
	defer s.deploymentMu.Unlock()
	if old, err := s.cfg.Store.RequestObject(r.Context(), "deployment", project, p.ownerKey(), body.RequestID); err == nil {
		var d savedDeployment
		if err := json.Unmarshal(old.Payload, &d); err != nil {
			apiError(w, 500, "storage_error", "cannot read deployment")
			return
		}
		if d.ApprovedPlan.ID != body.PlanID || body.Fingerprint != d.RequestFingerprint {
			apiError(w, 409, "idempotency_conflict", "request_id was used with a different plan")
			return
		}
		writeJSON(w, 200, d.Deployment)
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		apiError(w, 500, "storage_error", "cannot read request")
		return
	}
	o, err := s.cfg.Store.Object(r.Context(), "plan", body.PlanID)
	if err != nil || o.Project != project || o.Owner != p.ownerKey() {
		apiError(w, 404, "plan_not_found", "plan unavailable for this credential")
		return
	}
	var plan api.Plan
	if err := json.Unmarshal(o.Payload, &plan); err != nil {
		apiError(w, 500, "storage_error", "cannot read plan")
		return
	}
	if !time.Now().Before(plan.ExpiresAt) {
		apiError(w, 409, "plan_expired", "prepare a new plan")
		return
	}
	if plan.Action == "destroy" {
		if !p.isAdmin() || body.ConfirmProject != project {
			apiError(w, 403, "destruction_confirmation_required", "administrator and exact confirm_project are required")
			return
		}
	} else if plan.Action == "deploy" {
		if !p.canPublish() {
			apiError(w, 403, "maintainer_required", "maintainer token required")
			return
		}
	} else if !p.canDeploy() {
		apiError(w, 403, "deployer_required", "deployer token required")
		return
	}
	if active, err := s.activeDeployment(r.Context(), project); err != nil {
		apiError(w, 500, "storage_error", "cannot check active requests")
		return
	} else if active {
		apiError(w, 409, "project_busy", "a deployment is already active for this project")
		return
	}
	if err := s.validatePlanBase(r.Context(), plan); err != nil {
		apiError(w, 409, "stale_plan", err.Error())
		return
	}
	now := time.Now().UTC()
	id := "dep_" + deliveryNonce()
	targets := append([]api.Target{}, plan.Targets...)
	for i := range targets {
		if targets[i].Repository != "" {
			targets[i].DeliveryID = id + "-" + targets[i].Service
		}
		targets[i].State = "pending"
	}
	d := savedDeployment{Deployment: api.Deployment{RequestFingerprint: body.Fingerprint, SchemaVersion: 1, ID: id, Project: project, Status: "accepted", Step: "accepted", CreatedAt: now, UpdatedAt: now, Targets: targets}, ApprovedPlan: plan, TokenID: p.TokenID}
	b, _ := json.Marshal(d)
	if err := s.cfg.Store.CreateObject(r.Context(), store.Object{Kind: "deployment", ID: id, Project: project, Owner: p.ownerKey(), RequestKey: body.RequestID, Payload: b}); err != nil {
		apiError(w, 500, "storage_error", "cannot persist deployment")
		return
	}
	s.audit(r.Context(), p, project, "deployment.accepted", id)
	writeJSON(w, 202, d.Deployment)
}

func (s *Server) activeDeployment(ctx context.Context, project string) (bool, error) {
	objects, err := s.cfg.Store.Objects(ctx, "deployment", project)
	if err != nil {
		return false, err
	}
	for _, o := range objects {
		var d savedDeployment
		if err := json.Unmarshal(o.Payload, &d); err != nil {
			return false, err
		}
		if !api.Terminal(d.Status) {
			return true, nil
		}
	}
	return false, nil
}

func (s *Server) validatePlanBase(ctx context.Context, p api.Plan) error {
	raw, m, err := s.projectManifest(ctx, p.Project)
	if errors.Is(err, promote.ErrProjectNotFound) {
		raw = nil
		m = promote.ProjectManifest{}
	} else if err != nil {
		return fmt.Errorf("cannot read current manifest")
	}
	hash := ""
	if len(raw) > 0 {
		hash = spec.HashBytes(raw)
	}
	if hash != p.BaseRevision {
		return fmt.Errorf("project changed since this plan was prepared")
	}
	_, policyHash, err := s.policyFor(ctx, p.Project, m)
	if err != nil || policyHash != p.PolicyRevision {
		return fmt.Errorf("project policy changed")
	}
	return nil
}

func (s *Server) deploymentsV2(w http.ResponseWriter, r *http.Request) {
	p, project, ok := s.projectAccess(w, r)
	if !ok {
		return
	}
	objects, err := s.cfg.Store.Objects(r.Context(), "deployment", project)
	if err != nil {
		apiError(w, 500, "storage_error", "cannot list deployments")
		return
	}
	out := []api.Deployment{}
	for i := len(objects) - 1; i >= 0; i-- {
		var d savedDeployment
		if err := json.Unmarshal(objects[i].Payload, &d); err != nil {
			apiError(w, 500, "storage_error", "cannot read deployment")
			return
		}
		if deploymentVisible(p, d) {
			out = append(out, d.Deployment)
		}
	}
	writeJSON(w, 200, map[string]any{"deployments": out})
}
func deploymentVisible(p Principal, d savedDeployment) bool {
	if len(p.Services) == 0 {
		return true
	}
	for _, t := range d.Targets {
		allowed := false
		for _, name := range p.Services {
			if name == t.Service {
				allowed = true
			}
		}
		if !allowed {
			return false
		}
	}
	return len(d.Targets) > 0
}
func (s *Server) deploymentV2(w http.ResponseWriter, r *http.Request) {
	p, project, ok := s.projectAccess(w, r)
	if !ok {
		return
	}
	o, err := s.cfg.Store.Object(r.Context(), "deployment", r.PathValue("id"))
	var d savedDeployment
	if err != nil || o.Project != project || json.Unmarshal(o.Payload, &d) != nil || !deploymentVisible(p, d) {
		apiError(w, 404, "deployment_not_found", "deployment unavailable")
		return
	}
	writeJSON(w, 200, d.Deployment)
}

func (s *Server) deploymentLoop(ctx context.Context) {
	if s.cfg.Store == nil {
		return
	}
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		s.deploymentMu.Lock()
		objects, err := s.cfg.Store.Objects(ctx, "deployment", "")
		if err == nil {
			for _, o := range objects {
				var d savedDeployment
				if json.Unmarshal(o.Payload, &d) != nil || api.Terminal(d.Status) {
					continue
				}
				stepCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				if err := s.advanceDeployment(stepCtx, &d); err != nil {
					d.Status = "failed"
					d.Error = err.Error()
				}
				cancel()
				d.UpdatedAt = time.Now().UTC()
				if api.Terminal(d.Status) {
					event, _ := json.Marshal(map[string]any{"credential": d.TokenID, "action": "deployment." + d.Status, "deployment_id": d.ID, "project": d.Project, "step": d.Step, "infra_commit": d.InfraCommit, "at": d.UpdatedAt})
					_ = s.cfg.Store.CreateObject(ctx, store.Object{Kind: "audit", ID: deliveryNonce(), Project: d.Project, Owner: o.Owner, Payload: event})
				}
				b, _ := json.Marshal(d)
				o.Payload = b
				_ = s.cfg.Store.SaveObject(ctx, o)
			}
		}
		s.deploymentMu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

func (s *Server) advanceDeployment(ctx context.Context, d *savedDeployment) error {
	p := d.ApprovedPlan
	if !d.CreatedAt.IsZero() && time.Since(d.CreatedAt) > 2*s.cfg.Wait {
		return fmt.Errorf("deployment exceeded the server convergence budget; inspect the recorded work before a new request")
	}
	if d.Step == "gitops_written" {
		return fmt.Errorf("interrupted after GitOps commit; inspect the recorded commit before a new request")
	}
	if d.Status == "accepted" {
		if d.TokenID > 0 {
			t, err := s.cfg.Store.APITokenByID(ctx, d.TokenID)
			if err != nil || !t.RevokedAt.IsZero() || (!t.ExpiresAt.IsZero() && !time.Now().Before(t.ExpiresAt)) {
				return fmt.Errorf("the authorizing credential is no longer valid")
			}
		}
		if err := s.validatePlanBase(ctx, p); err != nil {
			return err
		}
		if p.Action == "destroy" {
			_, m, err := s.projectManifest(ctx, p.Project)
			if errors.Is(err, promote.ErrProjectNotFound) {
				m = promote.ProjectManifest{}
			} else if err != nil {
				return err
			}
			policy, _, err := s.policyFor(ctx, p.Project, m)
			if err != nil {
				return err
			}
			if err := s.checkDestroy(ctx, p.Project, m, policy.Namespace); err != nil {
				return err
			}
			req := s.requestProject(ctx, p.Project)
			req.ExpectedManifestHash = &p.BaseRevision
			res, err := promote.DeleteProject(ctx, req)
			if err != nil {
				return err
			}
			d.InfraCommit = res.InfraCommitSHA
			d.Step = "gitops_removed"
			if err := s.reconcileApps(ctx, p.Project, nil); err != nil {
				return fmt.Errorf("gitOps removed; inventory cleanup failed: %w", err)
			}
			if err := s.deleteProjectTenant(ctx, p.Project, policy.Namespace); err != nil {
				return fmt.Errorf("gitOps removed; tenant cleanup failed: %w", err)
			}
			if err := s.cfg.Store.RevokeProjectTokens(ctx, p.Project); err != nil {
				return fmt.Errorf("project removed; token revocation failed: %w", err)
			}
			if err := s.cfg.Store.DeleteObject(ctx, "policy", p.Project); err != nil {
				return err
			}
			d.Status = "succeeded"
			d.Step = "completed"
			return nil
		}
		m, err := promote.ParseProjectManifest([]byte(p.Manifest))
		if err != nil {
			return err
		}
		if p.Action == "deploy" {
			req := s.requestProject(ctx, p.Project)
			req.Manifest = m
			req.Raw = p.Manifest
			req.ExpectedManifestHash = &p.BaseRevision
			res, err := promote.EnsureProject(ctx, req)
			if err != nil {
				return err
			}
			d.InfraCommit = res.InfraCommitSHA
			d.Step = "gitops_written"
			// Persist the completed external step before provisioning or queuing.
			if err := s.saveDeployment(ctx, *d); err != nil {
				return err
			}
			if err := s.ensureProjectTenant(ctx, res.Project, res.Namespace); err != nil {
				return fmt.Errorf("gitOps committed; tenant provisioning failed: %w", err)
			}
			if err := s.reconcileApps(ctx, res.Project, res.Apps); err != nil {
				return fmt.Errorf("gitOps committed; pipeline reconciliation failed: %w", err)
			}
			if err := s.syncProjectTenantScope(ctx); err != nil {
				return fmt.Errorf("gitOps committed; secret scope reconciliation failed: %w", err)
			}
		}
		for i := range d.Targets {
			t := &d.Targets[i]
			if t.Repository == "" || t.Removed {
				continue
			}
			app, err := s.lookupApp(ctx, t.App)
			if err != nil {
				return fmt.Errorf("service was not registered: %s", t.Service)
			}
			if p.Action == "deploy" {
				if b, err := s.cfg.Store.SucceededBuildByCommit(ctx, app.ID, t.Commit); err == nil && b.ImageDigest != "" {
					unchanged := true
					for _, change := range p.Changes {
						if change.Resource == "service/"+t.Service && change.Action != "unchanged" {
							unchanged = false
						}
					}
					if unchanged {
						t.DeliveryID = b.DeliveryID
						t.BuildID = b.ID
						if latest, err := s.cfg.Store.LatestProductionBuild(ctx, app.ID); err == nil && latest.CreatedAt.After(b.CreatedAt) && latest.CommitSHA != t.Commit && (latest.Status == "running" || latest.Status == "succeeded") {
							d.Status = "superseded"
							d.Step = "newer_revision_requested"
							return nil
						}

						continue
					}
				}
			}
			if err := s.cfg.Store.InsertDelivery(ctx, store.Delivery{ID: t.DeliveryID, AppID: app.ID, EventType: "redeploy", CommitSHA: t.Commit, Status: "queued"}); err != nil && !errors.Is(err, store.ErrDuplicate) {
				return err
			}
		}
		d.Status = "deploying"
		d.Step = "awaiting_builds"
		s.kick()
		return nil
	}
	// If a process stopped after committing GitOps but before persisting the
	// next phase, do not repeat that write with an obsolete approval.
	if d.Step == "gitops_written" {
		return fmt.Errorf("interrupted after GitOps commit; inspect the recorded commit before preparing a new request")
	}
	currentRaw, currentManifest, currentErr := s.projectManifest(ctx, d.Project)
	_ = currentRaw
	if currentErr == nil {
		expectedManifest, err := promote.ParseProjectManifest([]byte(p.Manifest))
		if err != nil {
			return err
		}
		if spec.Hash(currentManifest) != spec.Hash(expectedManifest) {
			d.Status = "superseded"
			d.Step = "configuration_replaced"
			return nil
		}
	} else if s.cfg.GitOpsURL != "" {
		return fmt.Errorf("cannot verify the requested configuration against GitOps")
	}
	allReady := true
	for i := range d.Targets {
		t := &d.Targets[i]
		if t.Removed {
			if s.cfg.Kube == nil {
				allReady = false
				continue
			}
			_, err := s.cfg.Kube.AppsV1().Deployments(t.Namespace).Get(ctx, t.Service, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				t.State = "removed"
			} else if err != nil {
				return fmt.Errorf("cannot observe removal of %s", t.Service)
			} else {
				allReady = false
			}
			continue
		}
		app, err := s.lookupApp(ctx, t.App)
		if err != nil {
			allReady = false
			continue
		}
		expectedImage := t.Image
		if t.Repository != "" {
			b, err := s.cfg.Store.BuildByDelivery(ctx, app.ID, t.DeliveryID)
			if errors.Is(err, store.ErrNotFound) {
				delivery, e := s.cfg.Store.Delivery(ctx, t.DeliveryID)
				if e == nil && delivery.Status == "failed" {
					return fmt.Errorf("service %s: %s", t.Service, delivery.Error)
				}
				allReady = false
				continue
			}
			if err != nil {
				return err
			}
			t.BuildID = b.ID
			if latest, err := s.cfg.Store.LatestProductionBuild(ctx, app.ID); err == nil && latest.CreatedAt.After(b.CreatedAt) && latest.CommitSHA != t.Commit && (latest.Status == "running" || latest.Status == "succeeded") {
				d.Status = "superseded"
				d.Step = "newer_revision_requested"
				return nil
			}
			t.State = b.Status
			if b.Status == "failed" || b.Status == "rolled_back" {
				d.Status = b.Status
				d.Error = b.Error
				return nil
			}
			if b.Status != "succeeded" {
				allReady = false
				continue
			}
			expectedImage = b.ImageRepository + ":" + b.ImageTag + "@" + b.ImageDigest
		}
		observation := s.observeApp(ctx, app)
		t.State = observation.State
		if observation.ConfigurationRevision != t.ConfigurationRevision || !sameDigest(observation.Image, expectedImage) {
			allReady = false
			continue
		}
		if observation.State == "failed" || observation.State == "degraded" {
			return fmt.Errorf("service %s: %s", t.Service, observation.Message)
		}
		if observation.State != "running" && observation.State != "stopped" {
			allReady = false
		}
	}
	if allReady {
		d.Status = "succeeded"
		d.Step = "completed"
	} else {
		d.Step = "awaiting_reconciliation"
	}
	return nil
}

func sameDigest(a, b string) bool {
	_, ad, okA := strings.Cut(a, "@")
	_, bd, okB := strings.Cut(b, "@")
	return okA && okB && ad != "" && ad == bd
}
func (s *Server) saveDeployment(ctx context.Context, d savedDeployment) error {
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return s.cfg.Store.SaveObject(ctx, store.Object{Kind: "deployment", ID: d.ID, Payload: b})
}
func (s *Server) audit(ctx context.Context, p Principal, project, action, id string) {
	b, _ := json.Marshal(map[string]any{"credential": p.TokenID, "name": p.Name, "action": action, "resource": id, "at": time.Now().UTC()})
	_ = s.cfg.Store.CreateObject(ctx, store.Object{Kind: "audit", ID: deliveryNonce(), Project: project, Owner: p.ownerKey(), Payload: b})
}

func (s *Server) requestV2(w http.ResponseWriter, r *http.Request) {
	p, project, ok := s.projectAccess(w, r)
	if !ok {
		return
	}
	o, err := s.cfg.Store.RequestObject(r.Context(), "deployment", project, p.ownerKey(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, 200, map[string]any{"deployment": nil})
		return
	}
	if err != nil {
		apiError(w, 500, "storage_error", "cannot resolve request")
		return
	}
	var d savedDeployment
	if json.Unmarshal(o.Payload, &d) != nil || !deploymentVisible(p, d) {
		apiError(w, 404, "request_not_found", "request unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"deployment": d.Deployment})
}
