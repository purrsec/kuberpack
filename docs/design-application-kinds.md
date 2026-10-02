# Design — assets front et sites statiques

Statut : **proposition**, non implémenté. Sert de base d'implémentation.

## Constat vérifié (Railpack 0.40.1)

Ce que l'exécuteur fait réellement, testé localement :

- **Provider statique intégré.** Un dépôt de fichiers avec `index.html` est détecté comme
  `Staticfile` → Railpack construit une image **Caddy** avec de bons défauts (gzip/zstd,
  en-têtes de sécurité, `hide .env*`, `hide .git`) et une commande de démarrage. Un site statique est
  donc **déjà** un « server » de bout en bout (image → digest → chart `stateless`).
- **Les `Dockerfile` sont ignorés.** Un dépôt ne contenant qu'un `Dockerfile` échoue :
  « Railpack could not determine how to build the app ». `dockerfile` **ne peut pas** être une
  stratégie Railpack.
- **`prepare` expose `--build-cmd` et `--start-cmd`** ; le répertoire racine et le dossier de
  publication ne sont pas des options CLI.
- La conf Caddy par défaut **n'a pas de fallback SPA** (elle gère une page 404, pas le
  `try_files … /index.html`).

## Le vrai problème

Ce n'est pas « servir du statique » : Railpack sait déjà le faire. Les manques réels sont :

1. **Un front construit à part.** Quand les assets sont produits par un build (Vite/Tailwind/Next)
   et/ou vivent dans un sous-dossier (monorepo), Railpack ne sait pas quoi construire ni quel
   dossier publier. C'est le cas d'ECMS (Django + Vite) : `app.css` n'existait pas dans l'image.
2. **Ces réglages ne sont pas exposés.** `root_directory` / `build_command` sont documentés mais
   **non câblés** (contrat fantôme) ; rien ne permet de désigner un dossier de publication.
3. **Pas de fallback SPA.** Une app front servie telle quelle renverra 404 au rechargement d'une route.

## Modèle proposé

Rester sur **l'exécuteur unique** (Railpack) et exposer sa configuration, plutôt que d'inventer une
chaîne parallèle.

### Champ `kind`

| kind | Sens |
| --- | --- |
| `server` (défaut) | Un process long dans l'image. Couvre le back **et** le statique (Caddy). |
| `static` | Sucre : mêmes build, mais s'assure qu'un serveur statique est le process (utile si le dépôt contient aussi du code serveur qu'on ne veut pas lancer). |

> Note : un site purement statique peut rester `server` — Railpack produit déjà Caddy. `kind: static`
> n'existe que pour forcer le mode quand la détection serait ambiguë.

### Réglages de build (le cœur du sujet)

| Champ | Défaut | Rôle |
| --- | --- | --- |
| `root_directory` | racine | sous-dossier construit (monorepo) |
| `build_command` | détecté | commande de build front, transmise à `railpack prepare --build-cmd` |
| `publish_directory` | détecté | dossier de sortie à servir (`dist`, `build`, `out`, `public`, …) |
| `spa` | `true` si `index.html` | fallback SPA |
| `start_command` | détecté | existant |

### Stratégie de service

| Cas | Mécanisme |
| --- | --- |
| Dépôt statique pur | provider statique Railpack (Caddy) — **existant** |
| Front + build (Vite…) | `build_command` + `publish_directory` ; servir le dossier publié |
| Front + back (Django + Vite) | `kind: server` : l'app sert ses assets (chemin ECMS) |
| Cas non couvert | `dockerfile` : **chemin de build séparé** (buildkit `dockerfile.v0`), pas Railpack |

Le serveur statique est **celui de Railpack (Caddy)**, pas un nginx maison : moins de choses à
posséder, et les défauts sont déjà bons. On n'ajoute que ce qui manque : le choix du dossier publié
et le fallback SPA.

## Modes d'échec couverts

| Cas | Comportement |
| --- | --- |
| SPA rechargée sur une route profonde | fallback par défaut (config Caddy fournie) |
| Front présent, build non détecté | échec explicite demandant `build_command` |
| Monorepo | `root_directory` + `publish_directory` |
| Dossier publié introuvable | **échec avant push**, Git inchangé, message actionnable |
| Dépôt Dockerfile-only | bascule sur le chemin `dockerfile` (buildkit), pas Railpack |
| App avec API + front | `kind: server`, l'app sert ses assets |

## Critères d'acceptation

1. Un dépôt Vite/React sans Dockerfile ni fichier plateforme se déploie : assets présents, SPA OK au
   rechargement, image pinnée par digest.
2. Un dépôt statique pur reste déployable (provider Railpack / Caddy) — **non-régression**.
3. `root_directory` et `build_command` ont un effet **réel** (contrat câblé).
4. `publish_directory` introuvable → échec avant push, production inchangée.
5. Aucun `railpack.json`/`Procfile` exigé pour `auto`.
6. Previews : même build, mêmes garanties.
7. Le chart `stateless` n'est pas modifié.

## Étapes d'implémentation

1. **Câbler `root_directory` / `build_command`** (bug isolé, utile seul).
2. Ajouter `publish_directory` + `spa` ; config Caddy (fallback SPA) dans l'image statique.
3. `dockerfile` comme chemin de build séparé dans `buildexec` (buildkit `dockerfile.v0`).
4. Test de bout en bout : un site statique puis un front Vite, plus la non-régression `server`.

## Hors périmètre

- Service statique externe / CDN, `dockercompose`, contrôle direct d'un cluster.
- Réimplémenter un serveur statique : on utilise celui de Railpack.
