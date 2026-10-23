package netcheck

import (
	"fmt"
	"regexp"
	"strings"
)

// Le nom du contexte est le seul signal fiable disponible avant de contacter
// l'API. Les marqueurs courants sont reconnus comme des frontières de mots :
// "product" ne doit pas être confondu avec "prod".
var productionContextPattern = regexp.MustCompile(`(?i)(^|[._-])(prod|production|prd|live)([._-]|$)`)

// IsProductionContext indique si un nom de contexte ressemble à une production.
func IsProductionContext(name string) bool {
	return productionContextPattern.MatchString(strings.TrimSpace(name))
}

// ConfirmProbe refuse un contexte implicite et exige --yes pour toute probe.
// Le diagnostic écrit un namespace, des pods et une policy : même sur un banc
// jetable, l'opérateur doit désigner et confirmer la cible.
func ConfirmProbe(contextName, server string, yes bool) error {
	contextName = strings.TrimSpace(contextName)
	if contextName == "" {
		return fmt.Errorf("contexte Kubernetes explicite requis via --context : le diagnostic refuse le contexte courant implicite")
	}
	if yes {
		return nil
	}
	if IsProductionContext(contextName) {
		return fmt.Errorf("le contexte %q ressemble à une production (%s) : le diagnostic crée un namespace temporaire et des pods. Confirmer explicitement avec --yes", contextName, displayServer(server))
	}
	return fmt.Errorf("le diagnostic écrit un namespace temporaire, des pods et une NetworkPolicy sur %q (%s). Confirmer avec --yes", contextName, displayServer(server))
}

func displayServer(server string) string {
	if strings.TrimSpace(server) == "" {
		return "adresse inconnue"
	}
	return server
}
