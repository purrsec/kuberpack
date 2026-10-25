package runtimecli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/reloader"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func runReloader(args []string) error {
	fs := flag.NewFlagSet("reloader", flag.ContinueOnError)
	kubeconfig := fs.String("kubeconfig", "", "kubeconfig path (defaults to the in-cluster configuration)")
	interval := fs.Duration("interval", 30*time.Second, "how often the consumed Secrets are checked")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected reloader arguments: %v", fs.Args())
	}
	var (
		config *rest.Config
		err    error
	)
	if *kubeconfig != "" {
		config, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
	} else {
		config, err = rest.InClusterConfig()
	}
	if err != nil {
		return err
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return reloader.Run(ctx, client, *interval, func(format string, values ...any) {
		fmt.Fprintf(os.Stderr, "kuberpack reloader: "+format+"\n", values...)
	})
}
