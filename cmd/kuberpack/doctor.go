package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/netcheck"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// runDoctor implemente `kuberpack doctor network`. Le contexte est obligatoire
// pour ne jamais écrire par accident dans le cluster courant. La probe réelle
// n'est exécutée qu'après --yes ; --detect-only ne modifie rien.
func runDoctor(args []string) error {
	if len(args) == 0 || args[0] != "network" {
		return fmt.Errorf("usage: kuberpack doctor network --context NOM [--kubeconfig FICHIER] [--detect-only | --yes]")
	}
	fs := flag.NewFlagSet("doctor network", flag.ContinueOnError)
	contextName := fs.String("context", "", "contexte Kubernetes explicitement désigné (obligatoire)")
	kubeconfig := fs.String("kubeconfig", "", "chemin du kubeconfig (défaut : règles standard)")
	yes := fs.Bool("yes", false, "confirme l'écriture du namespace temporaire et des pods de test")
	detectOnly := fs.Bool("detect-only", false, "détecte le CNI et affiche les commandes, sans écriture")
	image := fs.String("image", netcheck.DefaultImage, "image busybox accessible depuis le cluster cible")
	timeout := fs.Duration("timeout", 3*time.Minute, "durée maximale de la probe")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("arguments inattendus: %s", strings.Join(fs.Args(), " "))
	}
	contextNameValue := strings.TrimSpace(*contextName)
	if contextNameValue == "" {
		return fmt.Errorf("--context est obligatoire : le diagnostic refuse le contexte courant implicite")
	}
	if *timeout <= 0 {
		return fmt.Errorf("--timeout doit être positif")
	}

	client, config, err := doctorClient(*kubeconfig, contextNameValue)
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	fmt.Printf("Contexte Kubernetes : %s (%s)\n", contextNameValue, config.Host)
	if netcheck.IsProductionContext(contextNameValue) {
		fmt.Println("Attention : ce contexte ressemble à une production ; le diagnostic reste en lecture seule tant que --yes n'est pas fourni.")
	}

	detection, detectErr := netcheck.Detect(ctx, client)
	if detectErr != nil {
		fmt.Fprintf(os.Stderr, "Détection CNI incomplète : %v\n", detectErr)
	}
	fmt.Print(netcheck.RenderDetection(detection))
	fmt.Println()

	if *detectOnly {
		fmt.Print(netcheck.RenderManual(detection))
		return nil
	}
	if err := netcheck.ConfirmProbe(contextNameValue, config.Host, *yes); err != nil {
		return err
	}

	report, probeErr := netcheck.RunProbe(ctx, client, netcheck.ProbeOptions{Image: *image, Timeout: *timeout})
	if report.Namespace != "" {
		fmt.Print(netcheck.RenderProbe(report))
	}
	fmt.Print(netcheck.RenderManual(detection))
	if probeErr != nil {
		return probeErr
	}
	if report.CleanupFailed {
		return fmt.Errorf("le namespace temporaire %q n'a pas pu être supprimé : nettoyer manuellement", report.Namespace)
	}
	switch report.Verdict {
	case netcheck.VerdictEnforced:
		return nil
	case netcheck.VerdictNotEnforced:
		return fmt.Errorf("NetworkPolicy non appliquée : le deny-all n'a pas bloqué le trafic (moteur réseau, agent ou mode à vérifier)")
	default:
		return fmt.Errorf("probe réseau non concluante : le contrôle positif ou le deny-all n'a pas produit d'observation fiable")
	}
}

// doctorClient construit un client sur le contexte nommé. Aucun repli vers le
// contexte courant : le diagnostic doit porter sur une cible désignée.
func doctorClient(kubeconfig, contextName string) (kubernetes.Interface, *rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if strings.TrimSpace(kubeconfig) != "" {
		rules.ExplicitPath = kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: contextName}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("kubeconfig ou contexte %q: %w", contextName, err)
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("client Kubernetes: %w", err)
	}
	return client, config, nil
}
