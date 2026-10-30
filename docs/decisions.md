# Décisions

Choix techniques du sizer ; ils sont révisables. Une décision nouvelle s'ajoute à la suite.

## T1 — Un seul binaire Go, front embarqué

- Demande de l'utilisateur (2026-10-27) : faire du sizer (une page HTML autonome) un outil comme
  aerospike-wellness.
- Binaire `sizer` (module `github.com/Hytm/aerospike-sizer`), sous-commandes `serve`, `deploy`, `status`,
  `uninstall`, `version`.
- Front en HTML, CSS et JS vanilla embarqués (`//go:embed`, `internal/web/static`), sans chaîne npm.
  L'ancienne copie `index.html` (version antérieure au découplage Active-rack / Local read) est supprimée :
  il ne reste qu'une interface.
- Calculateur seul (choix de l'utilisateur) : aucun scénario n'est enregistré, rien n'est écrit sur disque
  hors du service lui-même.

## T2 — Service launchd à deux exemplaires

- Agents `fr.sizer.app.a` / `fr.sizer.app.b`, un seul en service, écoute `127.0.0.1:7310`
  (`SIZER_ADDR`) ; `make run` écoute `127.0.0.1:7311`. Chaque exemplaire a sa copie du binaire
  (`~/Library/Application Support/sizer/{a,b}/sizer`), journaux dans `~/Library/Logs/sizer/`.
- `sizer deploy` installe la nouvelle version dans l'exemplaire libre, qui démarre sur le **même port**
  (`SO_REUSEPORT`, activé seulement par `SIZER_SHARED_PORTS=1` dans le plist) ; bascule quand `/health`
  répond avec le **pid** et la version du nouveau ; l'ancien reçoit `SIGTERM`, finit ses requêtes (au plus
  `DrainSeconds`, repris en `ExitTimeOut`) et sort sans être relancé (`KeepAlive` → `SuccessfulExit`
  à `false`). Si le nouveau ne répond pas, il est retiré et l'ancien reste en service.
- Pas de travail de fond ni de données partagées : aucun verrou `flock` n'est nécessaire.
- Code repris d'aerospike-wellness (`internal/service`), tests compris.

## T3 — Tests et CI

- `make ci` : `gofmt`, `go vet`, `staticcheck`, `go test -race`, build, lancé en local avant chaque merge.
  Pas de CI GitHub (demande de l'utilisateur, 2026-10-27).

## T4 — Modèle de calcul en Go, interface sur une API

- Le dimensionnement, le trafic réseau et la comparaison de coûts sont calculés côté serveur
  (`internal/sizing`, `internal/network`, `internal/plan`), testés en Go ; le front n'affiche que ce que
  renvoie `POST /api/plan` (le formulaire entier en une requête, réponse JSON avec les explications).
  `GET /api/defaults` donne le catalogue et le formulaire d'ouverture.
- L'API n'accepte que `Content-Type: application/json` : un autre site ne peut pas y poster sans
  requête préalable, que le serveur ne permet jamais. Fournisseur, région et instance sont validés contre
  le catalogue avant toute recherche de prix.
- Les paramètres de cache des fichiers statiques sont l'empreinte de leur contenu (`assetsVersion`) : la
  version d'un build de développement ne change pas d'un build à l'autre.

## T5 — Hypothèses alignées sur aerospike-wellness

- Choix de l'utilisateur (2026-10-27) : le comparateur déclarait « OK » des machines remplies jusqu'à
  85 % du NVMe brut, alors qu'Aerospike passe en stop-writes à 70 % (`stop-writes-used-pct`). Les
  hypothèses reprennent celles de wellness (T22) : stockage 70 % (50 % depuis T9, repris par wellness
  dans sa décision T30), RAM 70 % après 5 Gio réservés à l'OS (10 % de la RAM, 5 Gio au moins, depuis
  T9), CPU 60 % de 15 000 transactions/s par
  vCPU, IO 100 % des IOPS soutenues, N+1, nœuds multiples du nombre de racks. Toutes sont modifiables
  dans l'interface (badge « what-if »).
- Le CPU s'ajoute aux ressources (comme dans wellness) : sans lui, le comparateur recommandait des nœuds
  de 2 à 4 vCPU pour des centaines de milliers de transactions par seconde. Le nœud générique a un champ
  vCPU (16 par défaut).
- Croissance : appliquée aux données (stockage, RAM), pas au débit, comme le sizer d'origine
  (remplacé par T11 : le débit aussi).
- Unités : stockage en décimal (TB, comme les disques sont vendus), RAM en binaire (GiB, comme les
  fournisseurs la publient) ; l'ancienne page affichait en binaire des saisies décimales.
- Usage affiché : part de la capacité brute (RAM hors réserve OS) avec le plafond sous chaque jauge.
  Statut d'une configuration : charge rapportée à la capacité utilisable des nœuds restants après la
  perte d'un nœud ; au-delà de 100 % « insufficient », au-delà de 85 % « tight ».
- Recommandation par fournisseur : la configuration confortable (tout sous 85 %) la moins chère aux prix
  de référence, en ajoutant des nœuds à un gabarit tant que c'est moins cher qu'un gabarit plus grand,
  avec la pénalité de wellness sur les grands nombres de nœuds. Une configuration tight ou insuffisante
  reçoit une alternative confortable à nombre de nœuds égal quand un autre gabarit résout le problème
  (pas pour un problème de topologie). Les alternatives sont recalculées à chaque requête : elles ne
  s'accumulent plus pendant la saisie comme dans l'ancienne page.

## T6 — Prix live côté serveur

- L'ancienne page interrogeait les prix depuis le navigateur : AWS (ec2.shop) lisait un champ `Price`
  qui n'existe pas (le prix est dans `Cost`) et prenait le premier résultat à défaut ; Azure (Retail
  Prices API) n'envoie pas d'en-tête CORS, et le minimum retenu aurait été le prix Spot. Les prix
  « live » n'aboutissaient donc jamais.
- Le serveur interroge ec2.shop (correspondance exacte du type d'instance) et l'API Azure (Linux, à la
  demande, hors Spot, Low Priority et Windows), en tâche de fond : la première réponse est « pending »
  avec le prix de référence, l'interface redemande jusqu'à obtenir le prix. Cache 12 h, nouvel essai
  10 min après un échec, bouton ↻ pour forcer. Google Cloud n'a pas de source publique gratuite : prix
  de référence.
- Prix de référence : ceux du catalogue de wellness (US East, relevés le 2026-10-21), affichés comme tels.
  `SIZER_OFFLINE=1` coupe les recherches live.

## T7 — Trafic réseau

- Rack-aware : une écriture envoie ses répliques à `min(RF, AZ) − 1` autres racks (les copies au-delà
  d'une par rack restent dans un rack qui en a déjà une) ; l'ancien calcul comptait `RF − 1` traversées.
  Sans rack-awareness, chaque réplique traverse avec la probabilité `(AZ − 1)/AZ` (l'ancien calcul :
  toujours).
- Active Rack ne s'engage que si chaque rack a une copie complète (AZ ≤ RF) : sinon le trafic est calculé
  sans lui, avec un avertissement. Désactivé par défaut (RF2 sur 3 AZ).
- XDR : le coût de calcul compte un cluster par région ; le trafic inter-AZ dans chaque région n'est pas
  inclus (dit dans la note).

## T8 — Interface unifiée avec wellness

- Demande de l'utilisateur (2026-10-27) : revoir l'interface et l'unifier avec wellness. Mêmes jetons de
  couleur (clair et sombre selon le système), bandeau, panneaux, KPI, jauges avec plafond, badges et
  pastilles de nature (« estimated », « live », « reference ») ; lien vers wellness dans le bandeau.
- Formulaire à gauche (charge, nœud générique, hypothèses), résultats à droite (nœuds, KPI, placement
  par rack, réseau), comparaison de coûts en pleine largeur dessous. Une recommandation dont on change les
  nœuds devient une configuration à part, gardée telle quelle.

## T9 — Modèle aligné sur le guide de capacité Aerospike

- Demande de l'utilisateur (2026-10-27) : un outil aussi juste et complet que possible, avec le
  dimensionnement all-flash et les index secondaires. Constantes relues le 2026-10-27 dans le guide
  « Capacity planning » (aerospike.com/docs/server/operations/plan/capacity) et « Secondary index
  capacity planning » (aerospike.com/docs/database/manage/planning/capacity/secondary-indexes) ; les
  exemples chiffrés du guide sont des tests (`internal/sizing/sizing_test.go`).
- **Index primaire** : 64 B par copie en RAM. **All-flash** (`index-type flash`) : un sprig est un bloc
  de 4 KiB de 64 entrées ; `partition-tree-sprigs` = enregistrements ÷ (64 × remplissage) ÷ 4096,
  arrondi à la puissance de deux supérieure ; espace flash = 4096 partitions × RF × sprigs × 4 KiB,
  plus 5 B par sprig (racines écrites à l'arrêt) ; 10 B de RAM par sprig (13 avant 5.7). Remplissage
  50 % par défaut, comme l'exemple du guide (4 milliards d'enregistrements RF2 → 32 768 sprigs par
  partition, 1 TiB sur flash, 2,5 GiB de RAM). L'IO de l'index flash est une estimation du sizer, pas
  du guide : une lecture de bloc par lecture, une lecture et une écriture par copie écrite.
- **Index secondaires** (6.0+) : 14 B par entrée (8 de valeur ou de hachage, 6 de référence) ×
  sparseness 1,5 en moyenne (2 au pire) × RF ; une entrée par bin scalaire indexé, une par élément
  distinct d'une liste ou d'une map ; saisie en « entrées par enregistrement, tous index confondus ».
  En RAM ou sur flash (`sindex-type`). **Index de set** : 16 B par copie. Non comptés : 16 MiB
  d'allocation initiale par index et par nœud, étages de 1 Gio (`sindex-stage-size`).
- **Moteur mémoire** : enregistrements et index en RAM (même format qu'en 7.0+) ; « persisté » ajoute
  les enregistrements sur disque et les IOPS d'écriture ; sans persistance, aucune IO disque.
- **Disque** : le guide dit de planifier au plus 50 % d'usage avec `defrag-lwm-pct` par défaut →
  plafond 50 % et surcoût d'écriture 0 par défaut (le 70 % + 10 % d'origine double-comptait la
  défragmentation). Wellness a repris ces deux valeurs (sa décision T30, hypothèses du 2026-10-27) et
  transmet ses hypothèses (plafonds, croissance, N+1…) au sizer telles quelles : les deux outils donnent
  le même nombre de nœuds, aux constantes du sizer près (conventions de croissance, de plancher et de
  réserve OS alignées en T11).
  **Compression** (EE) : part de la taille stockée, disque seulement. **Surcoût d'enregistrement** : 0
  par défaut, la taille saisie étant celle stockée (en-tête 39 B + bins, alignement 16 B selon le
  guide) ; à renseigner quand on saisit la seule charge utile.
- **Réserve OS** : 10 à 15 % de la RAM selon le guide → 10 % par défaut, avec un plancher de 5 Gio.
- **Perte planifiée** : rien, un nœud (N+1) ou un rack (une AZ) : les plafonds tiennent sur les nœuds
  restants (nœuds − ⌈nœuds ÷ racks⌉ pour un rack), pour le CPU et les IOPS comme pour la capacité,
  comme le guide le demande pour le débit.
- Non modélisé : pmem, tombstones (durable deletes, transactions SC), les 8 blocs d'écriture réservés
  par device, le cache post-écriture.

## T10 — Pré-remplissage par l'URL

- `GET /?<chemin>=<valeur>…` : un paramètre par champ, nommé comme dans l'API (`workload.objects`,
  `workload.index_on`, `hypotheses.growth`, `network.azs`, `node.ram_gib`…) ; `source` nomme l'origine
  affichée, `back` (URL locale seulement) ramène à la page d'origine. C'est ainsi que Wellness renvoie
  au sizer avec les paramètres d'un cluster (décision T29 de Wellness).

## T11 — Croissance aussi sur le débit, plancher RF+1 et réserve OS partagés avec wellness

- Choix de l'utilisateur (2026-10-27), pour que les deux outils donnent le même nombre de nœuds :
  la croissance prévue sur l'horizon porte sur les enregistrements **et** sur le débit, au même taux
  (convention de wellness, que le sizer adopte ; T5 est remplacé sur ce point). CPU, IOPS disque
  (lectures, écritures, blocs de l'index flash) et trafic de réplication sont calculés sur le débit à
  l'horizon (`Workload.Grown`, `Detail.TPS`, affiché sur la carte CPU).
- Plancher de nœuds : RF+1 (convention du sizer, reprise par wellness dans sa décision T31, qui
  abandonne son minimum de 3 nœuds). Réserve OS : wellness adopte 10 % de la RAM avec un plancher de
  5 Gio (T31) et la transmet au sizer.
- Les deux outils donnent désormais le même nombre de nœuds pour un même gabarit et les mêmes
  hypothèses, aux constantes propres au sizer près (sprigs, index secondaires, surcoût
  d'enregistrement, compression), que wellness transmet explicitement.
- Effet sur l'exemple par défaut (500 M × 1 KiB, 200 000 lectures/s + 40 000 écritures/s, nœud
  16 vCPU / 128 Gio) : 312 000 tx/s à l'horizon → 20,8 vCPU et 213 200 IOPS ; le nœud générique passe
  de 3 nœuds (plancher RF+1) à 6 (CPU, N+1 arrondi à 3 racks) ; recommandations AWS i4i.xlarge × 12
  (× 9 avant), GCP z3-highmem-22-highlssd × 3 (z3-highmem-16-highlssd × 3 avant), Azure L8s_v3 × 9
  (L16s_v3 × 3 avant) ;
  trafic inter-AZ 466 452 Go/mois (358 810 avant).
- `network.Estimate` garde sa signature : le plan lui passe la charge à l'horizon (`Workload.Grown`),
  le trafic suit donc la croissance sans que le paquet réseau la connaisse.
- Le nombre d'AZ accepté par l'API passe de 6 à 12 (`network.Settings.Normalize`), pour qu'un cluster
  rack-aware de plus de six racks analysé par wellness arrondisse ses nœuds aux mêmes racks ici.

## T12 — IO disque comptée en blocs de 4 Kio

- Demande de l'utilisateur (2026-10-30), après comparaison avec la feuille « Aerospike Sizing
  Estimator » : le sizer comptait une IO par lecture et une par copie écrite, quelle que soit la taille
  des enregistrements. Il sous-estimait la lecture des gros enregistrements (12 Ko : 3 blocs, comptés 1)
  et surestimait l'écriture des petits (300 o × RF2 : moins d'un dixième de bloc, compté 2).
- L'unité est le bloc de 4 Kio, celle des IOPS soutenues du catalogue (lectures aléatoires de 4 Kio).
  Taille stockée d'une copie = (taille + surcoût) × compression.
  - **Lecture** : ⌈taille stockée ÷ 4 Kio⌉ blocs (`blocks_per_read`), × (1 − cache).
  - **Écriture** : les enregistrements remplissent des blocs d'écriture de 8 Mio, le disque écrit leurs
    octets : écritures × RF × taille stockée ÷ 4 Kio × défragmentation.
  - Index flash inchangé (des blocs de 4 Kio déjà).
- Pas de contrainte de débit (Mo/s) séparée : comptées en blocs de 4 Kio, les IOPS bornent aussi le
  débit (IOPS × 4 Kio), et aucune source ne donne le débit soutenu de chaque instance (la feuille prend
  1 500 Mo/s par disque pour toutes).
- Écarts restants avec la feuille, voulus : elle arrondit les écritures à des blocs de 8 Mio entiers par
  seconde à l'échelle du cluster (au plus 2 048 IOPS de plus), ne compte ni cache ni défragmentation
  (hypothèses du sizer, modifiables), compte 5 IO par copie écrite avec l'index sur flash (2 ici) et
  multiplie une seconde fois par le RF les IOPS d'écriture de son total.
- Wellness compte de même depuis sa décision T34 (2026-10-30), à partir de la taille moyenne mesurée
  telle que stockée (transmise avec un surcoût de 0 et une compression de 1) : les deux outils donnent
  le même nombre de nœuds quand les IOPS dimensionnent (T11), index primaire en RAM (wellness ne compte
  pas d'IO pour l'index sur flash).
- Effet sur l'exemple par défaut (500 M × 1 Kio, 200 000 lectures/s + 40 000 écritures/s, +30 %) :
  213 200 → 111 800 IOPS (écritures 135 200 → 33 800) ; nœuds et recommandations inchangés (le CPU
  dimensionne).

## T13 — Instances GCP Axion (C4A, ARM) avec Titanium SSD

- Demande de l'utilisateur (2026-10-30) : dimensionner sur GCP en privilégiant les instances ARM. Le
  catalogue GCP n'avait que des Z3 (x86) ; il gagne les `c4a-standard-*-lssd` et `c4a-highmem-*-lssd`
  (Google Axion, arm64) de 4 à 72 vCPU : 4 Gio (standard) ou 8 Gio (highmem) par vCPU, 1 à 16 disques
  Titanium SSD de 375 Gio (402,65 Go) selon la taille (doc Compute Engine, « Local SSD », table C4A).
- Prix de référence us-east1 à la demande, comme le reste du catalogue GCP : 0,03085 $ par vCPU·h,
  0,0035125 $ par Gio·h et 0,0616 $ par disque·h (0,000164 $ par Gio·h, SSD obligatoire et facturé en
  plus de la machine). Décomposition tirée des prix publiés (gcloud-compute.com, 2026-10-30) des
  c4a-highmem-16 (0,943 $/h ; -lssd 1,1896 $/h), c4a-highmem-72-lssd (5,23 $/h), c4a-standard-4
  (0,1796 $/h) et c4a-highcpu-4 (0,1515 $/h), et vérifiée sur la c4a-highmem-32-lssd (2,2559 $/h publié,
  2,2560 $/h calculé). Prix identiques en us-central1.
- IOPS de planification : la moitié des IOPS d'écriture aléatoire publiées, soit 37 500 par disque
  (150 000 en lecture et 75 000 en écriture publiés par disque). Hypothèse prudente, faute de mesure
  ACT pour ces disques ; à revoir avec une mesure.
- Les instances d'un fournisseur restent triées par prix : C4A et Z3 s'intercalent.
- Effet sur l'exemple par défaut : GCP recommande c4a-highmem-4-lssd × 12 (7 270 $/mois) au lieu de
  z3-highmem-22-highlssd × 3 (11 782 $/mois) ; AWS et Azure inchangés.
