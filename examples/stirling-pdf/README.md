# Stirling PDF avec un seul manifeste

Le seul fichier de déploiement écrit par le développeur est
[`kuberpack.yaml`](kuberpack.yaml). Adapter son hostname au domaine de la
plateforme. L'image officielle est épinglée par digest lors de la compilation.

La configuration suit la [notice Docker de Stirling](https://docs.stirlingpdf.com/Server/Install/Docker/)
: volume persistant pour `/configs` et `/storage`, et limite mémoire de 4 Go.
La [sonde officielle](https://docs.stirlingpdf.com/Configuration/Automation/Usage%20Monitoring/)
est `/api/v1/info/status`. L'image initialise ses répertoires en root puis
change d'utilisateur ; ses permissions nécessaires sont déclarées dans le
manifeste. Le login reste activé.

## Préparer les secrets

Avec un compte développeur autorisé sur le projet Infisical de la plateforme :

```bash
infisical login --domain "$INFISICAL_DOMAIN"
infisical secrets set --file "$HOME/.config/kuberpack/stirling.env" \
  --domain "$INFISICAL_DOMAIN" --projectId "$INFISICAL_PROJECT_ID" --env prod
```

Le fichier local, conservé hors du dépôt, contient les trois clés :

```dotenv
SECURITY_INITIALLOGIN_USERNAME=<identifiant>
SECURITY_INITIALLOGIN_PASSWORD=<mot-de-passe>
SECURITY_CUSTOMGLOBALAPIKEY=<cle-API-de-test>
```

Stirling utilise les identifiants initiaux uniquement lorsque sa base
utilisateurs est vide. Ils sont ensuite conservés dans `/configs`.
La clé API permet de vérifier une véritable opération PDF avec le login activé.
Voir la [configuration de l'authentification](https://docs.stirlingpdf.com/Configuration/Security/System%20and%20Security/)
et la [documentation API](https://docs.stirlingpdf.com/Developers/API/).

## Compiler

Configurer `KUBERPACK_API_URL` et `KUBERPACK_API_TOKEN` avec les informations
fournies par l'opérateur, puis :

```bash
kuberpack project compile -f examples/stirling-pdf/kuberpack.yaml --dry-run
kuberpack project compile -f examples/stirling-pdf/kuberpack.yaml
kuberpack apps
```

Aucun `kubectl apply`, chart spécifique ou YAML Kubernetes supplémentaire
n'est nécessaire au développeur. Le control plane écrit le manifeste et ses
ressources générées dans le dépôt GitOps configuré ; Flux les applique.

## Vérification fonctionnelle

Le test du dépôt ne parle qu'à l'URL HTTPS de l'application. Il vérifie le
login, le refus d'une opération anonyme et une fusion authentifiée de deux
PDF, dont le nombre de pages est contrôlé avec `pdfinfo` (`poppler-utils`).

```bash
python3 testenv/scripts/stirling-smoke.py \
  --url https://pdf.151.115.39.23.nip.io \
  --secrets-file "$HOME/.config/kuberpack/stirling.env" \
  --state-file /tmp/stirling-storage-test.json
```

Au premier passage, il enregistre aussi un PDF dans le stockage de Stirling.
Après un redéploiement déclenché par le manifeste, rejouer la même commande
vérifie que ce fichier est conservé avec la même empreinte. Le fichier d'état
ne contient que son identifiant et son empreinte, aucun secret.
