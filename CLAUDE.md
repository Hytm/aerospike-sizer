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
- **Dimensionnement partagé entre aerospike-sizer et aerospike-wellness** : pour un même gabarit et les
  mêmes hypothèses, les deux outils donnent le même nombre de nœuds (sizer T11 ; wellness T31).
  Conventions communes :
  - hypothèses par défaut : croissance 30 %, stockage 50 % sans surcoût d'écriture, RAM 70 % après une
    réserve OS de max(5 Gio, 10 % de la RAM), CPU 60 % de 15 000 transactions/s par vCPU, IO 100 % des
    IOPS de planification, défragmentation 1,3, aucun cache de lecture, perte d'un nœud (sizer T5, T9,
    T15, T18 ; wellness T6, T22, T30, T31, T43) ;
  - hypothèses bornées de même : plafonds de 5 à 100 %, cache d'au plus 100 %, croissance de 0 à
    +10 000 %, surcoût d'écriture de 0 à 10, défragmentation de 1 à 100, 100 à 10 000 000 transactions
    par vCPU, réserve OS d'au plus 1 000 000 Gio et 90 % de la RAM (sizer T14, T18 ; wellness T31) ;
  - croissance appliquée aux enregistrements et au débit, au même taux (sizer T11 ; wellness T31) ;
  - plancher de RF + 1 nœuds ; RF = copies stockées, de 1 à 8 : 0 lu 1, au-delà de 8 le sizer en
    planifie 8 et wellness le note (sizer T11, T17, T18 ; wellness T31) ;
  - perte à survivre : aucune, un nœud (défaut) ou un rack (nœuds − ⌈nœuds ÷ racks⌉, jamais moins de
    nœuds que pour un nœud ; sans racks, celle d'un nœud) ; racks = AZ rack-aware, 12 au plus ; nœuds
    arrondis à un multiple des racks (sizer T5, T9, T11, T14 ; wellness T6, T31) ;
  - gabarit inadapté quand une ressource demande plus de 10 000 nœuds en service (sizer T18 ;
    wellness T44) ;
  - débit : une sous-transaction batch compte comme une transaction (sizer T18 ; wellness T44) ;
  - IO disque en blocs de 4 Kio : lecture de ⌈taille stockée ÷ 4 Kio⌉ blocs sur les seules lectures qui
    trouvent un enregistrement, hors cache, aucun bloc sans enregistrement maître ayant des octets sur
    le disque (moteur device, objets > 0, taille stockée > 0), toutes les lectures comptant pour le CPU
    et le réseau ; écriture des octets de chaque copie × défragmentation (sizer T12, T17, T18 ; wellness
    T31, T34, T46) ;
  - cache de lecture nul par défaut, prérempli avec le `cache_read_pct` mesuré, au dixième (sizer T15,
    T16 ; wellness T43) ;
  - enregistrements en mémoire (7.0+) au plafond de stockage, index au plafond RAM (sizer T15 ;
    wellness T43) ;
  - index primaire : octets par copie effectifs (tous ses octets ÷ objets maîtres × RF, sans plancher de
    64 B), chaque index placé d'un bloc là où sont la plupart de ses octets, jamais sur flash avec le
    moteur mémoire, pmem planifié en mémoire (sizer T18 ; wellness T46) ;
  - index primaire sur flash planifié selon le guide : blocs de sprigs de 4 Kio remplis à 50 %, sprigs par
    partition en puissance de deux de 256 à 2²⁸ tirés des enregistrements maîtres à l'horizon au plus
    grand nombre de copies, 10 B de RAM par sprig, IO de l'index (sizer T9, T20 ; wellness T48) ;
  - IOPS de planification : la moitié des IOPS d'écriture aléatoire publiées (règle T13 du sizer) ;
    60 000 par disque pour les Azure Lsv3 et Lasv3 (sizer T13, T15, T16, T19 ; wellness T43, T44, T47) ;
  - un même catalogue dans les deux outils : mêmes types et mêmes chiffres (gabarit, IOPS de
    planification, prix de référence lus en US East), tenus égaux par un test de parité de chaque côté
    (sizer T19 ; wellness T47) ;
  - usage affiché : part de la capacité brute, RAM hors réserve OS, tous les nœuds en service (sizer T5 ;
    wellness T22) ;
  - arrondi par excès des ratios à 10⁻¹² près (sizer T18 ; wellness T46) ;
  - bornes jamais silencieuses : toute valeur que le sizer ramène dans ses bornes revient dans `clamps`,
    que wellness dit dans son panneau (sizer T18 ; wellness T46) ;
  - choix de la machine, une seule règle : par fournisseur, parmi les types offerts dans la région, la
    configuration confortable (chaque charge à 85 % au plus de son plafond, sinon celle qui tient) de plus
    petit score prix de référence × nœuds × (1 + nœuds ÷ 50), à égalité le moins de nœuds puis le nom ;
    wellness classe au prix d'un engagement d'un an en US East et le transmet au sizer (`pricing`) ; le
    sizer seul ouvre au prix à la demande (sizer T5, T21 ; wellness T23, T49) ;
  - textes du sizer : chaque explication, contrôle et note de l'API porte un code et ses valeurs, que
    wellness rédige en français et en anglais (repli sur l'anglais du sizer pour un code inconnu) ; un
    code ne change jamais de sens (sizer T22 ; wellness T50) ;
  - Un changement de convention se fait dans les deux outils (`~/work/aerospike-sizer` et
    `~/work/aerospike-wellness`) et se consigne dans les deux `docs/decisions.md` ; cette liste est la
    même dans les deux `CLAUDE.md`.

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
