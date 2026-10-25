package runtimecli

import (
	"errors"
	"flag"
	"fmt"
	"k8s.io/client-go/rest"
	"os"
	"strings"
)

func exit(err error) {
	if err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func ControlPlane()                     { exit(runServe(os.Args[1:])) }
func Reloader()                         { exit(runReloader(os.Args[1:])) }
func kubeConfig() (*rest.Config, error) { return rest.InClusterConfig() }
func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
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
