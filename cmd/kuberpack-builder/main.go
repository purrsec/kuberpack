package main

import (
	"context"
	"fmt"
	"git.host.bzh/pepe/kuberpack/internal/buildexec"
	"os"
	"strings"
)

func main() { runBuilder() }
func exit(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func getenvBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	return v == "1" || strings.EqualFold(v, "true")
}
func runBuilder() {
	if len(os.Args) != 1 {
		exit(fmt.Errorf("the builder is an internal worker and takes no arguments"))
	}
	_, err := buildexec.Run(context.Background(), buildexec.Request{
		CloneURL: os.Getenv("KUBERPACK_CLONE_URL"), CommitSHA: os.Getenv("KUBERPACK_COMMIT_SHA"),
		ImageRepository: os.Getenv("KUBERPACK_IMAGE_REPOSITORY"), RegistryUser: os.Getenv("KUBERPACK_REGISTRY_USER"),
		StartCmd: os.Getenv("KUBERPACK_START_CMD"), RootDirectory: os.Getenv("KUBERPACK_ROOT_DIRECTORY"),
		BuildCommand: os.Getenv("KUBERPACK_BUILD_CMD"), BuildsGitURL: os.Getenv("KUBERPACK_BUILDS_GIT_URL"),
		AppName: os.Getenv("KUBERPACK_APP_NAME"), Environment: os.Getenv("KUBERPACK_ENVIRONMENT"),
		GenerateLock: getenvBool("KUBERPACK_GENERATE_LOCK", false), GitAuthScheme: os.Getenv("KUBERPACK_GIT_AUTH"),
		CloneAnonymous: getenvBool("KUBERPACK_GIT_ANONYMOUS", false), BuildsGitAuthScheme: os.Getenv("KUBERPACK_BUILDS_GIT_AUTH"),
		AcceptedVulnerabilities: splitList(os.Getenv("KUBERPACK_TRIVY_IGNORE")),
	})
	if err != nil {
		buildexec.WriteTermination(err.Error())
		exit(err)
	}
}

func splitList(raw string) []string {
	var out []string
	for _, s := range strings.Split(raw, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
