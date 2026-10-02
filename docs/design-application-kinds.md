# Design — assets front et sites statiques

Statut : **proposition**, non implémenté. Sert de base d'implémentation.

## Constat vérifié (Railpack 0.40.1)

Ce que l'exécuteur fait réellement, testé localement :

- **Provider statique intégré.** Un dépôt de fichiers avec `index.html` est détecté comme
  `Staticfile` → Railpack construit une image **Caddy** avec de bons défauts (gzip/zstd,
  en-têtes de sécurité, `hide .env*`, `hide .git`) et une commande de démarrage. Un site statique est
  donc **déjà** un « server » de bout en bout (image → digest → chart `stateless`).
- **Un front Vite est pris en charge nativement.** Détection Node, `npm run build`,
  « vite static site », sortie `dist`, servi par Caddy. La conf générée :
  - écoute `:{$PORT:80}` → le chart impose `PORT`, donc **OK** ;
  - `respond /health 200` → le healthcheck par défaut `/health` marche sans config ;
  - **fallback SPA** : `try_files {path} {path}.html {path}/index.html /index.html` ;
  - en-têtes de sécurité, gzip/zstd, `hide .env*`/`.git`.
  Un front pur Vite/React se déploie donc **déjà** avec la stratégie `server` actuelle, sans aucun
  champ nouveau.
- **Les `Dockerfile` sont ignorés.** Un dépôt ne contenant qu'un `Dockerfile` échoue :
  « Railpack could not determine how to build the app ». `dockerfile` **ne peut pas** être une
  stratégie Railpack.
- **Variables non-`config-file`** : `RAILPACK_STATIC_FILE_ROOT` (racine servie pour un dépôt de
  fichiers) et `RAILPACK_SPA_OUTPUT_DIR` (sortie d'un front). Attention : lu par le frontend
  BuildKit (`railpack-frontend`), pas forcément par `railpack prepare` local — leur effet réel se
  vérifie au build.

## Le vrai problème

Ce n'est pas « servir du statique » ni « servir un front » : Railpack fait déjà les deux. Les
manques réels sont bien plus étroits :

1. **Un front produit à part du back.** Cas ECMS (Django + Vite) : c'est au back de servir les
   assets ; Railpack ne construit pas le front et l'image ne les contient pas. Ça reste à la charge
   de l'app (règle « auto-suffisante »), mais ce n'est pas outillé.
2. **Un front « custom » non reconnu.** Un projet Node sans script standard (le `RAILPACK_SPA_OUTPUT_DIR`
   n'agit pas sur `prepare`, et un dossier de sortie non standard fait échouer la détection).
3. **Le contrat d'override.** `root_directory` / `build_command` étaient documentés mais non câblés
   (corrigé, #13). Il reste à exposer un moyen fiable de désigner la sortie d'un front custom.

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
| Dépôt statique pur | provider statique Railpack (Caddy) — **existant, rien à faire** |
| Front Vite/React | détection Node + `npm run build` + Caddy — **existant, rien à faire** |
| Front « custom » (sortie non standard) | `build_command` + sortie (à câbler) |
| Front + back (Django + Vite) | `kind: server` : l'app sert ses assets (chemin ECMS) |
| Cas non couvert | `dockerfile` : **chemin de build séparé** (buildkit `dockerfile.v0`), pas Railpack |

Le serveur statique est **celui de Railpack (Caddy)**, avec fallback SPA et `/health` déjà fournis.
Il n'y a donc presque rien à construire : le seul vrai manque est le cas « front custom ».

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

1. Un dépôt Vite/React se déploie sans Dockerfile ni fichier plateforme : assets présents, SPA OK au
   rechargement, image pinnée par digest. (Attendu **gratuit** : Railpack le fait déjà.)
2. Un dépôt statique pur reste déployable (provider Railpack / Caddy) — **non-régression**.
3. `root_directory` et `build_command` ont un effet **réel** — fait (#13).
4. Un front « custom » peut désigner sa sortie.
5. Aucun `railpack.json`/`Procfile` exigé.
6. Previews : même build, mêmes garanties.
7. Le chart `stateless` n'est pas modifié.

## Étapes d'implémentation

1. ~~Câbler `root_directory` / `build_command`~~ — fait (#13).
2. **Vérifier en réel** qu'un front Vite et un site statique se déploient déjà (aucun code) : c'est
   le cas nominal. Rien à implémenter s'ils passent.
3. **Cas « front custom »** : exposer un moyen fiable de désigner la sortie (probablement une
   variable de build `RAILPACK_SPA_OUTPUT_DIR` lue par le frontend BuildKit) — à valider au build.
4. `dockerfile` comme chemin de build séparé dans `buildexec` (buildkit `dockerfile.v0`).
5. Test de bout en bout : un site statique **et** un front Vite, plus la non-régression `server`.

## Hors périmètre

- Service statique externe / CDN, `dockercompose`, contrôle direct d'un cluster.
- Réimplémenter un serveur statique : on utilise celui de Railpack.
