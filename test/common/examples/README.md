# Vérification HTTPS de Stirling

Cet exemple manuel ne fait pas partie des suites CI : il utilise une instance
Stirling existante et les credentials fournis par son développeur. Il fusionne
deux PDF et peut vérifier la persistance d’un document entre deux passages.
Il ne lit aucun kubeconfig.

```console
docker build -t kuberpack-stirling-check test/common/examples
docker run --rm kuberpack-stirling-check --help
```

Pour le lancer, monter le fichier de credentials en lecture seule et un
répertoire réservé à son état :

```console
mkdir -p .test-results/stirling-state
docker run --rm \
  --mount type=bind,src="$PWD/stirling.env",dst=/run/stirling.env,readonly \
  --mount type=bind,src="$PWD/.test-results/stirling-state",dst=/state \
  kuberpack-stirling-check \
  --url https://stirling.example.com \
  --secrets-file /run/stirling.env --state-file /state/document.json
```

Le fichier de credentials reste local et ne doit pas être commité. Les champs
attendus sont `SECURITY_INITIALLOGIN_USERNAME`,
`SECURITY_INITIALLOGIN_PASSWORD` et `SECURITY_CUSTOMGLOBALAPIKEY`.
L’état contient l’identifiant du document et
son empreinte ; utiliser le même état au passage suivant pour vérifier que le
document est toujours disponible.
