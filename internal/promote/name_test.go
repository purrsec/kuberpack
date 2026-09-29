package promote

import "testing"

func TestAppName(t *testing.T) {
	tests := []struct {
		path, repo, want string
	}{
		{"apps/web/values.yaml", "git.host.bzh/pepe/other", "web"},
		{"web/values.yaml", "git.host.bzh/pepe/other", "web"},
		{"values.yaml", "git.host.bzh/pepe/site", "site"},
		{"apps/values.yaml", "registry/my-app", "my-app"},
	}
	for _, tt := range tests {
		if got := AppName(tt.path, tt.repo); got != tt.want {
			t.Errorf("AppName(%q, %q)=%q want %q", tt.path, tt.repo, got, tt.want)
		}
	}
}
