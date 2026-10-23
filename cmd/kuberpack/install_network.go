package main

import (
	"context"
	"fmt"

	"git.host.bzh/pepe/kuberpack/internal/netcheck"
)

// detectNetwork identifie le moteur réseau avant toute écriture. Le résultat
// est conservé pour le plan et le compte rendu ; une détection partielle ne
// bloque pas l'installation, mais elle est affichée avec ses limites.
func (i *installation) detectNetwork(ctx context.Context) {
	detection, err := netcheck.Detect(ctx, i.kube)
	i.network = &detection
	fmt.Println()
	fmt.Print(netcheck.RenderDetection(detection))
	if err != nil {
		fmt.Printf("Avertissement : détection réseau partielle : %v\n", err)
	}
	if detection.Engine == netcheck.EngineUnknown || !detection.Enforced() {
		fmt.Println("AVERTISSEMENT RÉSEAU : l'application réelle des NetworkPolicy n'est pas établie.")
		fmt.Println("  Un objet NetworkPolicy présent dans l'API peut être ignoré par le CNI.")
		fmt.Println("  Vérifier : kuberpack doctor network --context <contexte> --yes")
		fmt.Println("  Matrice et limites : docs/network-matrix.md")
	}
	fmt.Println()
}

// printNetworkSummary rappelle la détection et la seule preuve acceptable :
// une probe de trafic réelle, pas la lecture des objets NetworkPolicy.
func (i *installation) printNetworkSummary() {
	if i.network == nil {
		return
	}
	fmt.Printf("Réseau : %s ; application des NetworkPolicy : %s.\n", i.network.Engine.Label(), i.network.Enforcement)
	fmt.Println("Vérification réelle recommandée : kuberpack doctor network --context <contexte> --yes (namespace temporaire, aucun Secret lu).")
}
