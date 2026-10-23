# Tests et gate de release

Ce document fixe la stratégie de test. Il distingue ce qui doit rester
unitaire, ce qui doit bloquer une PR, ce qui doit bloquer une release et ce qui
peut attendre le nightly. Il est volontairement honnête sur les temps : un
« test complet en 3-4 minutes » n'est pas réalisable à froid sur un runner
hébergé.

## Verdict

Un gate de **3-4 minutes** est réaliste pour un **contract gate** :
fixture k3s/Forgejo chaude, Kuberpack installé à froid, compilation et
déploiement d'une application **préconstruite**, assertions API/tenant/HTTP.
Il ne peut pas inclure une installation froide complète, un build Railpack réel,
un scan Trivy/Syft et les tests de sécurité complets.

Un **release gate** honnête prend **10-15 minutes** : cluster neuf, installation
complète (Flux, cert-manager, CNPG, Infisical), build réel, digest/pin, réseau,
reloader, réinstallation.

Un **nightly** de 30-45 minutes couvre l'upgrade N-1→N, la matrice CNI, les
addons, les previews, la persistance et, plus tard, EKS/GKE/AKS.

Si le budget 4 minutes doit inclure **installation froide + build réel**, il
faut un runner **self-hosted chaud** (images, volumes et caches persistants).
Sur un runner GitHub hébergé, le parcours complet est plutôt 6-10 minutes.

## Les trois niveaux

| Niveau | Ce qu'il prouve | Durée | Déclencheur |
| --- | --- | ---: | --- |
| Unitaire | parsing, rendu YAML, RBAC, admission CEL, pin/digest, store, API httptest | < 30 s à chaud | chaque commit |
| Contract gate | installation sur fixture chaude, compilation, Flux/Helm, HTTP/TLS, tenant, réseau minimal, réinstallation | 3-4 min | PR same-repo, `main` |
| Release gate | cluster neuf, installation froide complète, build+scan, secrets, réseau complet, reloader, réinstallation | 10-15 min | tag/release |
| Nightly | upgrade/rollback, CNI Calico/Cilium, addons, previews, persistance, EKS/GKE | 30-45 min | nightly/weekly |

Le contract gate ne remplace pas le release gate. Il attrape les régressions
rapides ; le release gate attrape les régressions d'installation et de build.

## Budget temps

Estimations issues du banc actuel et des scripts existants.

### Contract gate

| Phase | Hébergé chaud | Hébergé froid | Self-hosted chaud |
| --- | ---: | ---: | ---: |
| Images préchargées (k3s, Forgejo, control, builder, busybox) | 20-30 s | 45-70 s | 5-10 s |
| k3s + Forgejo prêts | 15-20 s | 25-35 s | 8-12 s |
| Seed PKI/tokens/dépôts | 8-12 s | 12-15 s | 5-8 s |
| `kuberpack install` rapide (dépendances chaudes) | 60-75 s | 90-120 s | 40-60 s |
| Compile + Flux forcé + HTTP | 30-40 s | 45-60 s | 20-30 s |
| Assertions + rapport | 10-15 s | 15-20 s | 8-12 s |
| **Total avec marge** | **2:45-3:40** | **4:25-6:08** | **1:40-2:32** |

La préparation de la fixture (k3s, Forgejo, Flux, cert-manager, CNPG,
Infisical) est **hors budget** : elle doit être un snapshot ou un cache. Si
elle est refaite à chaque run, le gate dépasse 4 minutes.

### Release gate

| Phase | Hébergé chaud | Hébergé froid | Self-hosted chaud |
| --- | ---: | ---: | ---: |
| Contract gate | 3:00-4:00 | 4:30-6:00 | 1:40-2:30 |
| Build Railpack + BuildKit + Trivy + Syft | 60-120 s | 150-200 s | 40-80 s |
| Déploiement Flux après pin | 25-40 s | 30-45 s | 20-30 s |
| Assertions digest/SBOM/HTTP | 10-15 s | 15-20 s | 8-12 s |
| **Total** | **4:35-7:25** | **7:05-9:25** | **2:45-4:15** |

Une installation **froide** complète (aucun opérateur préinstallé) est plutôt
8-15 minutes. C'est le nightly ou le release gate, pas la PR.

## Fixture

Le banc `testenv/` actuel est un bon environnement de développement, pas un
gate CI : IP fixes `172.29.90.0/24`, ports hôte 80/443/3000/2222/6443, noms de
conteneurs fixes, volumes persistants, tmpfs 20 GiB, outils requis sur l'hôte.

Le gate doit être **hermétique** :

```text
hôte / runner Linux (Docker, cgroup v2)
│
├── réseau kpg-<runid>  (aucun port publié)
│   ├── kpg-<runid>-forgejo  Forgejo 17 + registre OCI HTTPS
│   ├── kpg-<runid>-k3s      k3s épinglé (privileged, cgroupns host)
│   └── kpg-<runid>-runner   kubectl/helm/flux/git/python, sans socket Docker
│
├── volumes kpg-<runid>-{forgejo,k3s,artifacts}
└── images préchargées / snapshot de base
```

- **k3s** : image épinglée par digest. L'image actuelle (Debian + binaire k3s,
  sans systemd) est éprouvée ; l'image officielle `rancher/k3s` est plus légère
  et supporte l'airgap, mais doit être validée avant de remplacer le banc.
- **CA** : générée par run, montée dans k3s/Forgejo, SAN `forgejo`, `k3s`,
  `localhost`, IP du slot. Pas d'ACME dans le gate.
- **Réseau** : un réseau par run, sous-réseau choisi au runtime, aucune
  publication de port sur l'hôte. Les IP fixes internes sont possibles, mais
  le sous-réseau ne doit pas être codé en dur.
- **Stockage** : tmpfs pour `/var/lib/kubelet` sur btrfs ; pas de 20 GiB par
  défaut sur un runner hébergé.
- **Isolation** : conteneurs, volumes, réseau, dépôts, projet et hôtes
  préfixés par le `runid` ; lock par slot ; cleanup par labels/TTL.
- **Attentes** : `kubectl wait` sur node Ready, Traefik, CoreDNS, CRDs Flux,
  webhook cert-manager, HelmRelease Ready. Jamais de `sleep` comme assertion.
- **Flux** : forcer la réconciliation (`reconcile.fluxcd.io/requestedAt`) après
  chaque commit au lieu d'attendre l'intervalle.

## Contenu du contract gate

| # | Étape | Assertion bloquante |
| --- | --- | --- |
| 0 | Sanity fixture | node Ready, Forgejo API 200, `deploy/kuberpack` absent |
| 1 | `kuberpack install` | exit 0, `/healthz` 200, `/api/v1/apps` 401 sans jeton / 200 avec |
| 2 | Releases du socle | HelmRelease Ready, `observedGeneration == generation`, Kustomization Ready et révision = HEAD du socle |
| 3 | Secret humain + manifeste | login Infisical humain, compile `changed:true` puis `false` (JSON typé, pas substring) |
| 4 | Flux/Helm/HTTP | Kustomization projet Ready, HelmRelease Ready, Deployment `image=@sha256`, pod Ready, HTTPS avec CA, corps attendu |
| 5 | Tenant isolation | namespace possédé, SA sans token, RoleBinding exact, Kustomization `serviceAccountName/targetNamespace/path`, `can-i` oui dans son namespace / non dans `kube-system` |
| 6 | Réseau minimal | une probe autorisée, une probe refusée, DNS autorisé ; échec distingué d'une erreur DNS/pod absent |
| 7 | Réinstallation | HEAD socle/apps, hashes Secrets/PVC, UID Deployment inchangés ; aucun redémarrage ; API 200 |
| 8 | Rapport | `result.json`, `junit.xml`, timings, diagnostics même en échec |

Si le p95 dépasse 4 minutes, déplacer l'étape réseau en release/nightly plutôt
que d'allonger le gate.

## Contenu du release gate

- cluster neuf, aucune dépendance Kuberpack préinstallée ;
- installation complète (Flux, cert-manager, CNPG, Infisical, ingress/TLS) ;
- build réel Railpack + BuildKit + Trivy + Syft + push digest ;
- pin GitOps, Flux/Helm, HTTP/TLS ;
- secret humain et rotation réelle (reloader : pod remplacé, nouvelle valeur) ;
- `security-smoke` complet et `doctor network` ;
- réinstallation/interruption/reprise ;
- upgrade N-1→N et rollback.

## Ce qui reste unitaire

Tout ce qui n'a pas besoin d'un cluster : parsing strict du manifeste, rendu
Helm/Kustomize, RBAC/admission CEL, NetworkPolicy générée, pin/digest, store
SQLite, rate-limit, logs, handlers API avec fake client. La suite actuelle
(~386 tests) doit rester rapide ; elle ne remplace pas l'e2e.

## Trous actuels

- **Aucun e2e en CI** : `.github/workflows/image.yaml` ne fait que `go vet` et
  `go test`.
- **Banc non hermétique** : IP/ports/noms fixes, volumes partagés, outils hôte,
  tmpfs 20 GiB, hypothèse btrfs.
- **Chemins cassés après le renommage `charts/` → `provisioning/`** :
  `helmfile.yaml`, `deploy-kuberpack.sh`, `cloud-bootstrap.sh` et les filtres
  de `image.yaml` ont été corrigés ; il faut continuer à vérifier les docs et
  scripts.
- **Pas de build dans le gate actuel** : `install-smoke.py` déploie une image
  statique ; le build réel n'est pas prouvé.
- **Installation froide jamais testée** : cert-manager de test différent du
  produit, images importées dans containerd, donc `imagePullSecrets` et le
  premier pull registre ne sont pas exercés.
- **Reloader, upgrade/rollback, migration mono-dépôt → deux dépôts, CNI cloud,
  addons, previews** : rendus unitaires ou manuels, pas de gate.
- **Assertions faibles** : substring `"changed":false`, existence d'objets sans
  vérifier les specs, `can-i` en erreur compté comme refus, tout code non nul
  compté comme « denied ».
- **Artefacts d'échec absents** : `result.json` écrit seulement au succès,
  stdout/stderr supprimés dans `run()`.

## Assertions et artefacts

Chaque assertion doit porter `id`, `expected`, `actual`, `evidence`, `duration`.
Un échec doit dire quoi, où, et avec quelle commande. En cas d'échec, écrire
dans un `finally` :

- `result.json` et `junit.xml` ;
- `kubectl get all -A`, events triés, describe des objets en échec, YAML des
  HelmRelease/Kustomization/GitRepository ;
- logs control plane, Flux, builder, application, Forgejo, k3s ;
- `docker inspect`, `stats`, `df`, `free` ;
- diff GitOps et révisions ;
- `commands.log` avec code retour et durée, **redacté**.

Ne jamais écrire de Secret en clair : noms et hash uniquement. Prendre la
snapshot avant le cleanup.

## Flakes

- Pas de ports hôte, sous-réseau choisi au runtime, noms préfixés par le run.
- Attentes par conditions, pas par durée.
- Images épinglées et préchargées ; retries seulement sur les opérations
  idempotentes (pull, API 5xx), jamais sur une assertion.
- Forcer Flux après un commit.
- Timeout dur : 5 min contract gate, 8 min CI, 15 min release.
- Quarantaine par assertion, avec propriétaire, issue et expiration (14 jours).
  Jamais de quarantaine pour le tenant, les secrets, le deny réseau, le
  pin/digest ou l'admission dangereuse.

## Plan d'implémentation

### P0 — avant d'appeler cela un gate

1. Harnais hermétique : `testenv/gate/` (compose, `gate.sh`, PKI, seed Forgejo,
   attentes, cleanup, timings).
2. `result.json` + `junit.xml` + diagnostics, écrits en `finally`, secrets
   redactés.
3. Workflow CI : `unit` → `images` → `contract-gate` ; release gate séparé.
4. Assertions typées : `observedGeneration`, `lastAppliedRevision`, specs
   RoleBinding/Kustomization, digests, hashes, UID.
5. Mesurer 10 runs ; rendre required après 20 runs verts (p50 ≤ 3:30,
   p95 ≤ 5:00, zéro flake).

### P1 — 2-3 semaines

- build réel + scan en release/nightly ;
- security-smoke complet + doctor network ;
- reloader réel ;
- upgrade N-1→N + rollback ;
- installation froide, pull registre sans préchargement ;
- migration mono-dépôt → deux dépôts.

### P2 — plus tard

EKS/GKE/AKS, addons, previews, backup/restore, persistance, scale/soak,
ACME public, arm64.

## Décisions à prendre

1. **Contract gate 3-4 min** sur GitHub hébergé, avec fixture chaude et
   installation Kuberpack à froid (recommandé), ou **release gate complet
   3-4 min** sur self-hosted chaud (plus cher, plus fragile à maintenir).
2. **Snapshot de base chaude** (Forgejo/k3s/opérateurs déjà sains) ou gate
   froid nightly : le snapshot est nécessaire pour tenir 3-4 min.
3. **CA stable** pour le snapshot vs CA par run : la CA stable simplifie le
   snapshot mais doit être traitée comme un secret de test.
4. **Infisical/CNPG dans le contract gate** : oui si la base est chaude (teste
   le secret humain), sinon les déplacer en release/nightly.
5. **Build dans le contract gate** : non par défaut ; oui seulement avec un
   cache BuildKit chaud et une seconde lane parallèle.
