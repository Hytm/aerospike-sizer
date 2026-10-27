# Aerospike Sizer — consignes du projet

Outil local de dimensionnement d'un cluster Aerospike à partir d'un profil de charge : nombre de nœuds,
trafic réseau de réplication (multi-AZ ou XDR) et comparaison de coûts AWS / GCP / Azure. Binaire Go
unique, interface web en anglais, service launchd à deux exemplaires, sur le modèle
d'aerospike-wellness. Calculateur seul : rien n'est enregistré sur disque.

- **Décisions techniques** : [`docs/decisions.md`](docs/decisions.md). À relire avant toute implémentation ;
  une nouvelle décision s'y ajoute.
- **Dépôt** : `git@github.com:Hytm/aerospike-sizer.git` (**public** ; branche par défaut `main`). Aucune
  donnée client dans le dépôt.

## Workflow

Le workflow global (`~/.claude/DEV-WORKFLOW.md`) s'applique : une branche par demande depuis `main` à jour
(`feat/…`, `fix/…`, `refactor/…`, `chore/…`, `docs/…`), commits Conventional Commits, `make ci` vert avant
chaque push et chaque merge `--no-ff` dans `main`, puis `make deploy`. Pas de CI GitHub : la vérification est
locale (`make ci`), et on n'en ajoute pas.

## Conventions

- Go standard library d'abord ; pas de JS/TS côté serveur ; front en HTML/CSS/JS vanilla embarqué
  (`internal/web/static`).
- Interface en anglais seulement.
- **Crédibilité** : le dimensionnement est une estimation ; ses hypothèses sont affichées et modifiables.
  Un prix indique sa source (live, référence).
- **Interface unifiée avec aerospike-wellness** (décision T8) : reprendre ses jetons et composants
  (`internal/web/static/app.css`) plutôt qu'en inventer.

## Commandes

```bash
make ci        # gofmt, vet, staticcheck, tests (-race), build
make build     # bin/sizer
make run       # serveur local au premier plan sur 127.0.0.1:7311 (sans toucher au service)
make deploy    # installe/relève le service launchd (deux exemplaires, sans coupure)
./bin/sizer status | uninstall | version
```

- `SIZER_OFFLINE=1` : prix de référence seulement, aucune recherche live (repris par `make deploy`).
- Modèle : `internal/sizing` (nœuds, adéquation, recommandation), `internal/network` (trafic multi-AZ et
  XDR), `internal/pricing` (prix live), `internal/plan` (réponse de l'API) ; front dans `internal/web`.

## Déploiement

« Déployer » = `make deploy` depuis `main` à jour : construit le binaire et le passe à `sizer deploy`, qui
relève le service launchd sans coupure (décision T2). Interface : http://127.0.0.1:7310.
