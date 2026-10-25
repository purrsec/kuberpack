// Package api contains the wire contract shared by the HTTP client and server.
// It deliberately depends on neither Kubernetes nor the server implementation.
package api

import (
	"git.host.bzh/pepe/kuberpack/internal/spec"
	"time"
)

const Version = "2"

type Session struct {
	Version      string    `json:"api_version"`
	TokenID      int64     `json:"token_id"`
	Name         string    `json:"name"`
	Role         string    `json:"role"`
	Project      string    `json:"project,omitempty"`
	Services     []string  `json:"services,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	Capabilities []string  `json:"capabilities"`
	ForgeURL     string    `json:"forge_url,omitempty"`
}

type Project struct {
	Name      string    `json:"name"`
	Namespace string    `json:"namespace"`
	Revision  string    `json:"revision"`
	Manifest  string    `json:"manifest,omitempty"`
	Services  []Service `json:"services"`
}

type Service struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Project    string         `json:"project"`
	Repository string         `json:"repository,omitempty"`
	Branch     string         `json:"branch,omitempty"`
	Image      string         `json:"image,omitempty"`
	Hostname   string         `json:"hostname,omitempty"`
	Deployment map[string]any `json:"deployment,omitempty"`
}

type ProjectPolicy struct {
	Namespace         string               `json:"namespace" yaml:"namespace"`
	Infisical         *spec.InfisicalScope `json:"infisical,omitempty" yaml:"infisical,omitempty"`
	Domains           []string             `json:"domains,omitempty" yaml:"domains,omitempty"`
	Repositories      []string             `json:"repositories,omitempty" yaml:"repositories,omitempty"`
	AllowRoot         bool                 `json:"allow_root" yaml:"allow_root"`
	AllowWritableRoot bool                 `json:"allow_writable_root" yaml:"allow_writable_root"`
	Capabilities      []string             `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
	MaxServices       int                  `json:"max_services,omitempty" yaml:"max_services,omitempty"`
	MaxReplicas       int                  `json:"max_replicas,omitempty" yaml:"max_replicas,omitempty"`
}

type PlanRequest struct {
	Action           string  `json:"action"`
	Manifest         string  `json:"manifest,omitempty"`
	Service          string  `json:"service,omitempty"`
	Commit           string  `json:"commit,omitempty"`
	ExpectedRevision *string `json:"expected_revision,omitempty"`
}

type Target struct {
	Removed               bool   `json:"removed,omitempty"`
	Namespace             string `json:"namespace,omitempty"`
	Service               string `json:"service"`
	App                   string `json:"app"`
	Repository            string `json:"repository,omitempty"`
	Branch                string `json:"branch,omitempty"`
	Commit                string `json:"commit_sha,omitempty"`
	Image                 string `json:"image,omitempty"`
	ConfigurationRevision string `json:"configuration_revision"`
	DeliveryID            string `json:"delivery_id,omitempty"`
	BuildID               int64  `json:"build_id,omitempty"`
	State                 string `json:"state,omitempty"`
}

type Change struct {
	Resource string `json:"resource"`
	Action   string `json:"action"`
}

type Plan struct {
	ID             string    `json:"id"`
	Project        string    `json:"project"`
	Action         string    `json:"action"`
	BaseRevision   string    `json:"base_revision"`
	PolicyRevision string    `json:"policy_revision"`
	Manifest       string    `json:"manifest,omitempty"`
	Targets        []Target  `json:"targets"`
	Changes        []Change  `json:"changes"`
	ExpiresAt      time.Time `json:"expires_at"`
}

type DeploymentRequest struct {
	PlanID         string `json:"plan_id"`
	RequestID      string `json:"request_id"`
	ConfirmProject string `json:"confirm_project,omitempty"`
	Fingerprint    string `json:"fingerprint,omitempty"`
}

type Deployment struct {
	RequestFingerprint string    `json:"request_fingerprint,omitempty"`
	SchemaVersion      int       `json:"schema_version"`
	ID                 string    `json:"deployment_id"`
	Project            string    `json:"project"`
	Status             string    `json:"status"`
	Step               string    `json:"step"`
	Error              string    `json:"error,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
	InfraCommit        string    `json:"infra_commit,omitempty"`
	Targets            []Target  `json:"services"`
	Plan               Plan      `json:"-"`
}

func Terminal(status string) bool {
	return status == "succeeded" || status == "failed" || status == "rolled_back" || status == "superseded"
}

type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}
