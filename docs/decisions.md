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
  renvoie `POST /api/plan` (le formulaire entier en une requête, réponse JSON avec les explications ; un
  champ absent de la requête garde la valeur du formulaire d'ouverture, amendé le 2026-11-19, T18 :
  champs absents). `GET /api/defaults` donne le catalogue et le formulaire d'ouverture.
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
  perte d'un nœud ; au-delà de 100 % « insufficient », au-delà de 85 % « tight » (à 10⁻¹² près, amendé
  le 2026-11-19, T18).
- Recommandation par fournisseur : la configuration confortable (tout sous 85 %) la moins chère aux prix
  de référence, en ajoutant des nœuds à un gabarit tant que c'est moins cher qu'un gabarit plus grand,
  avec la pénalité de wellness sur les grands nombres de nœuds. Une configuration tight ou insuffisante
  reçoit une alternative confortable à nombre de nœuds égal quand un autre gabarit résout le problème
  (pas pour un problème de topologie). (amendé le 2026-11-19, T19 : recommandation et alternative sont
  prises parmi les types que la région de la ligne propose.) Les alternatives sont recalculées à
  chaque requête : elles ne s'accumulent plus pendant la saisie comme dans l'ancienne page.

## T6 — Prix live côté serveur

- L'ancienne page interrogeait les prix depuis le navigateur : AWS (ec2.shop) lisait un champ `Price`
  qui n'existe pas (le prix est dans `Cost`) et prenait le premier résultat à défaut ; Azure (Retail
  Prices API) n'envoie pas d'en-tête CORS, et le minimum retenu aurait été le prix Spot. Les prix
  « live » n'aboutissaient donc jamais.
- Le serveur interroge ec2.shop (correspondance exacte du type d'instance) et l'API Azure (Linux, à la
  demande, hors Spot, Low Priority et Windows), en tâche de fond : la première réponse est « pending »
  avec le prix de référence, l'interface redemande jusqu'à obtenir le prix. Cache 12 h, nouvel essai
  10 min après un échec, bouton ↻ pour forcer. Google Cloud n'a pas de source publique gratuite : prix
  de référence. (amendé le 2026-11-19, T19 : AWS est lu dans les fichiers de prix d'AWS, ec2.shop
  n'ayant ni i7ie ni i8g dans plusieurs des régions du sizer.)
- Prix de référence : ceux du catalogue de wellness (US East, relevés le 2026-10-21), affichés comme tels
  (amendé le 2026-11-19, T19 : tous relus le 2026-11-19, identiques dans les deux catalogues).
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
  de 4 KiB de 64 entrées ; `partition-tree-sprigs` = enregistrements ÷ (64 × remplissage) ÷ 4096, arrondi
  à la puissance de deux supérieure (le quotient arrondi par excès à 10⁻¹² près, amendé le 2026-11-19,
  T18) ; espace flash = 4096 partitions × RF × sprigs × 4 KiB, plus 5 B par sprig (racines écrites à
  l'arrêt) ; 10 B de RAM par sprig (13 avant 5.7). Remplissage 50 % par défaut, comme l'exemple du guide
  (4 milliards d'enregistrements RF2 → 32 768 sprigs par partition, 1 TiB sur flash, 2,5 GiB de RAM).
  L'IO de l'index flash est une estimation du sizer, pas du guide, que wellness compte de même (amendé le
  2026-11-19, T20) : une lecture de bloc par lecture, une lecture et une écriture par copie écrite.
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
  par device, le cache post-écriture (amendé le 2026-11-19, T18 : un index en pmem est planifié en RAM,
  aucune instance du catalogue n'ayant de pmem ; wellness replie les tombstones dans les octets d'index
  par copie effectifs qu'il envoie ; des besoins sans enregistrement maître ne se représentent pas, et
  wellness l'avertit).

## T10 — Pré-remplissage par l'URL

- `GET /?<chemin>=<valeur>…` : un paramètre par champ, nommé comme dans l'API (`workload.objects`,
  `workload.index_on`, `hypotheses.growth`, `network.azs`, `node.ram_gib`…) ; `source` nomme l'origine
  affichée, `back` (URL locale seulement) ramène à la page d'origine (amendé le 2026-11-19, T18 : le
  serveur la vérifie et accepte toute adresse loopback, 127.0.0.0/8, ::1 ou localhost ; sinon la page dit
  qu'il n'y a pas de lien retour). C'est ainsi que Wellness renvoie au sizer avec les paramètres d'un
  cluster (décision T29 de Wellness).

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
  hypothèses, aux constantes propres au sizer près (index secondaires, surcoût d'enregistrement,
  compression), que wellness transmet explicitement (amendé le 2026-11-19, T20 : les sprigs n'en sont
  plus, wellness dimensionne avec les mêmes constantes).
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
  - **Lecture** : ⌈taille stockée ÷ 4 Kio⌉ blocs (à 10⁻¹² près, amendé le 2026-11-19, T18)
    (`blocks_per_read`), × (1 − cache) (amendé le 2026-11-16, T17 : sur les seules lectures qui trouvent
    un enregistrement ; amendé le 2026-11-19, T18 : aucun bloc sans enregistrement ou avec des
    enregistrements de 0 octet).
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
  le même nombre de nœuds quand les IOPS dimensionnent (T11), index primaire en RAM ou sur flash, wellness
  comptant aussi les sprigs et l'IO d'index (amendé le 2026-11-19, T20).
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
- Les instances d'un fournisseur restent triées par prix : C4A et Z3 s'intercalent (amendé le
  2026-11-19, T19 : à prix égal, par nom).
- Effet sur l'exemple par défaut : GCP recommande c4a-highmem-4-lssd × 12 (7 270 $/mois) au lieu de
  z3-highmem-22-highlssd × 3 (11 782 $/mois) ; AWS et Azure inchangés.

## T14 — Revue complète : modèle, réseau, catalogue, service, interface

- Demande de l'utilisateur (2026-11-05) : le code le plus juste possible, sur les fonctionnalités comme sur
  la qualité. Revue par neuf lentilles (modèle, adéquation et recommandation, réseau, API et prix,
  interface, catalogue, qualité Go et tests, cohérence des textes, service launchd), chaque constat
  passé par trois vérificateurs contradictoires : 73 constats confirmés et corrigés, 12 écartés
  (branche `fix/revue-complete`, 2026-11-12).
- **Modèle** (`internal/sizing`) :
  - bornes hautes documentées sur toutes les entrées (taille ≤ 8 Mio, soit le bloc d'écriture ;
    enregistrements ≤ 10¹⁵ ; débit ≤ 10⁹/s ; nœud générique borné de même) : plus de +Inf ni de NaN, et
    une réponse non encodable répond désormais 500 au lieu d'un 200 vide (amendé le 2026-11-19, T18 :
    chaque borne appliquée est dite dans `clamps` de la réponse et sur la page ; entrées d'index
    secondaire bornées à 10⁹ par enregistrement au lieu de 10⁴ ; amendé le 2026-11-19, T18 : bornes
    basses et hypothèses, consignées dans la section des conventions partagées) ;
  - `partition-tree-sprigs` reste dans sa plage de configuration (référence : défaut et minimum 256,
    maximum 256M, puissance de deux ; au-delà de 32K, consulter le support Aerospike, ce que
    l'explication dit) : le sizer plafonnait à 65 536 et descendait jusqu'à 1 sprig, et son arrondi
    bouclait sans fin au-delà de 2⁶³ (`objects=1e24` avec l'index flash bloquait une goroutine à 100 %) ;
  - `index-type flash` est refusé avec `storage-engine memory` (référence de configuration, 7.0)
    (amendé le 2026-11-19, T18 : l'index est planifié en RAM et une note le dit ; le refus était
    silencieux) ;
  - la compression s'applique aussi aux enregistrements en mémoire (`compression` vaut pour
    `storage-engine memory` depuis 7.0) : T9 disait « disque seulement » ;
  - le nœud de secours est compté même quand un seul nœud porte une ressource (le besoin affiché par
    ressource passe de 1 à 2 sur l'exemple par défaut ; le nombre de nœuds ne change pas) ;
  - recommandation : le nombre de nœuds confortable d'un gabarit est calculé directement (demande ÷ 0,85
    passée à `Size`) au lieu d'une recherche bornée à 64 pas qui rendait une configuration « tight »
    au-delà de quelques centaines de nœuds ; « confortable » veut dire partout « toute charge sous
    85 % » (le seuil de 80 % propre aux alternatives disparaît) ; un gabarit dont une ressource est
    inutilisable (RAM entièrement réservée à l'OS) n'est plus proposé comme alternative, le suivant l'est.
- **Réseau** (`internal/network`) :
  - Azure ne facture plus le trafic entre zones de disponibilité depuis mai 2024 (Microsoft Learn,
    « Availability zones overview », section Inter-zone networking) : taux par défaut 0 ; sur
    l'exemple par défaut, Azure L8s_v3 × 9 (5 309 $/mois) devient la configuration la moins chère ;
  - XDR a ses propres taux inter-régions (`network.region_rates`, 0,02 $/Go par défaut, facturés en
    sortie par chaque fournisseur entre deux régions d'un même continent) au lieu des taux inter-AZ ;
    date de lecture des taux : `network.RatesAsOf` (2026-11-11), affichée ;
  - rack-aware avec RF > AZ : les copies au-delà d'une par rack sont placées au hasard (doc rack-aware :
    « the extra replicas are distributed randomly ») et traversent les AZ avec la probabilité
    (AZ − 1)/AZ ; T7 les comptait gratuites (RF3 sur 2 AZ : 2,0 × écritures × taille au lieu de 1,5) ;
  - hub and spoke : chaque écriture atteint les régions − 1 autres régions, comme en maillage, puisque
    chaque région détient une copie complète et paie un cluster entier (le facteur 1 sous-estimait le
    volume dès trois régions) ; la note XDR dit que chaque cluster régional est dimensionné pour le débit
    saisi ;
  - avertissements : sans rack-awareness, le placement par hachage met toutes les copies d'environ un
    enregistrement sur AZ^(RF−1) dans la même AZ ; l'avertissement RF > AZ parle d'AZ, pas de racks.
- **Catalogue** : les disques Z3 font 3 000 Gio (Google publie ses SSD locaux en Gio, comme pour C4A en
  T13) → 3 221,23 Go, soit 6,9 % de stockage de plus par nœud ; région GCP par défaut europe-west1
  (Belgique : europe-west9 n'offre ni Z3 ni C4A) ; date des prix de référence 2026-10-30 (amendé le
  2026-11-19, T19 : 2026-11-19, tous relus) ; les IOPS i4i, Z3 et Lsv3 sont celles du catalogue de
  wellness, dont la mesure n'est pas consignée (voir les choix ouverts) ; `tag` et `live_prices`
  quittent le catalogue, le livre de prix dit ce qu'il lit en direct.
- **Service** (`internal/service`) : la relève ne réutilise plus l'exemplaire en service quand une sonde
  `/health` échoue par hasard (sonde répétée trois fois ; si rien ne répond mais qu'un exemplaire est
  chargé par launchd, le déploiement refuse : `sizer uninstall` d'abord ; sinon les plists périmés sont
  retirés avant l'installation) ; un bootstrap raté retire son plist comme une relève ratée ; `status`
  ne lit plus les `state = active` des blocs de coalition ; un second signal pendant le drain termine le
  processus ; `service.Serve` isole et teste le cycle écoute → drain.
- **API et serveur** (`internal/web`) : hôte vérifié (421 pour un hôte autre que loopback ou l'adresse
  configurée, contre le DNS rebinding ; ajout à T4) ; `/api/defaults` dit quels fournisseurs sont lus
  en direct (`SIZER_OFFLINE=1` était invisible de l'interface), le seuil « tight » et la date des taux ;
  `Response.Defaults` supprimé ; `Generic.Checks` remonte les remarques topologiques du nœud générique
  (moins de nœuds que d'AZ sans rack-awareness) ; note quand la perte d'un rack ne peut pas être
  planifiée (pas de racks modélisés).
- **Interface** : une valeur hors boutons (RF 4 à 8, AZ 4 à 12, régions 5 à 8, préremplie par l'URL)
  reçoit un bouton synthétisé ; entiers préremplis arrondis ; lettres d'AZ jusqu'à l ; « no node count
  works » à la place de « undefined nodes needed » ; bandeau des prix et bouton ↻ fidèles au serveur ;
  saisie bornée aux limites des champs (amendé le 2026-11-19, T18 : les plafonds vont de 5 à 100 %,
  comme le serveur, et un test vérifie que chaque borne du formulaire est celle du serveur) ;
  accessibilité des segments (`aria-pressed`, nom de groupe).
- **Choix laissés à l'utilisateur** (non modifiés) : cache de lecture à 70 % par défaut (Aerospike n'a
  pas de cache de lecture par défaut ; valeur héritée du sizer d'origine et partagée avec wellness) ;
  moteur mémoire : données planifiées à 70 % de la RAM alors que 7.0+ les gère en blocs d'écriture
  préalloués avec stop-writes à 70 % et un plan à 50 % comme un device ; IOPS Z3 (50 000 par disque)
  contre la règle T13 appliquée aux C4A (moitié des écritures publiées, soit 250 000 par disque Z3).

## T15 — Cache de lecture nul, données en mémoire au plafond de stockage, IOPS des Z3

- Choix de l'utilisateur (2026-11-12) sur les trois points laissés ouverts par T14, appliqués ici et dans
  wellness (sa décision T43) pour que les deux outils gardent le même nombre de nœuds :
  - **Cache de lecture : 0 % par défaut.** Aerospike ne sert aucune lecture depuis un cache par défaut
    (le post-write cache ne garde que les écritures récentes, `read-page-cache` est désactivé) ; le
    70 % hérité du sizer d'origine surestimait les lectures absorbées de 3,3×. L'hypothèse devient
    « lectures servies par un cache », à renseigner avec le `cache_read_pct` mesuré d'un cluster réel.
    L'exemple par défaut passe de 111 800 à 293 800 IOPS (lectures 260 000) ; nœuds et recommandations
    ne changent pas (le CPU dimensionne).
  - **Données en mémoire planifiées au plafond de stockage.** En 7.0+, `storage-engine memory` range les
    enregistrements dans des blocs d'écriture préalloués (`data-size`), défragmentés et soumis à
    stop-writes comme un device : ils reçoivent le surcoût de blocs d'écriture et sont planifiés à
    `StorageFill` (50 %), les index restant à `RAMFill` (70 %). Le plafond RAM d'une demande est la
    moyenne harmonique pondérée des deux (`Hypotheses.ForDemand`), renvoyé dans `hypotheses.ram_fill`
    de la réponse pour que les jauges et les textes le montrent. Un namespace mémoire demande ainsi
    1,4× plus de mémoire pour ses enregistrements qu'avant.
  - **IOPS des Z3 : règle T13.** La moitié des IOPS d'écriture aléatoire publiées, soit 250 000 par
    disque Titanium SSD de 3 Tio (500 000 publiés) : 250 000 à 3 000 000 par nœud au lieu de 50 000 à
    550 000 (un disque Z3 valait 1,3 disque C4A alors que Google le donne 5× plus rapide). Les chiffres
    i4i et Lsv3 restent ceux du catalogue wellness, sans mesure consignée. (amendé le 2026-11-15, T16 :
    les i4i suivent désormais la règle T13 ; les Lsv3 gardent 60 000 par disque.)

## T16 — IOPS des i4i par la règle T13, cache de lecture mesuré envoyé par wellness

- Choix de l'utilisateur (2026-11-15, revue de wellness, sa décision T44) : toute instance du catalogue
  est planifiée à la moitié des IOPS d'écriture aléatoire publiées par son fournisseur (règle T13), pour
  que les deux outils gardent le même nombre de nœuds (T11, T15). Les i4i passent ainsi de 50 000 à
  500 000 IOPS par nœud à 13 750, 27 500, 55 000, 110 000, 220 000 et 440 000 : AWS publie 27 500 IOPS
  d'écriture pour le disque de 468 Go, 55 000 pour celui de 937 Go et 220 000 par disque de 3 750 Go
  (table « Instance store specifications » de docs.aws.amazon.com/ec2/latest/instancetypes/so.html,
  lue le 2026-11-15). Les anciens chiffres venaient du catalogue de wellness, sans mesure consignée.
- Z3 (250 000 par disque, T15) et C4A (37 500 par disque, T13) suivaient déjà la règle. Les Lsv3 gardent
  60 000 par disque (amendé le 2026-11-19, T19 : les Lasv3 aussi, entrées au catalogue) : Microsoft ne
  publie qu'un chiffre d'IOPS NVMe au mieux (400 000 par disque de 1,92 To, file de 256), sans chiffre
  d'écriture, ce qui ne permet pas d'appliquer la règle ; à revoir avec une mesure ACT.
- Effet sur l'exemple par défaut : AWS recommande toujours i4i.xlarge, mais 15 nœuds au lieu de 12 (les
  IOPS dimensionnent : 76 % de charge IOPS à 15 nœuds) ; GCP et Azure inchangés.
- wellness préremplit désormais l'hypothèse « lectures servies par un cache » avec le `cache_read_pct`
  mesuré du cluster, au dixième de point (moyenne pondérée par les lectures) ; le champ accepte donc une
  décimale (`step="0.1"`).

## T17 — Blocs de lecture sur les seules lectures qui trouvent un enregistrement, copies stockées envoyées par wellness

- Choix de l'utilisateur (2026-11-16, revue de wellness, sa décision T44) : une lecture qui ne trouve pas
  d'enregistrement est tranchée par l'index primaire et n'atteint aucun disque, mais le sizer lui
  comptait les blocs de 4 Kio d'une lecture (T12). Sur les clusters réels analysés par wellness, plus
  d'une centaine de mesures de namespaces sur disque ont plus de 30 % de lectures sans enregistrement,
  jusqu'à 94 % : leurs IOPS de lecture étaient surestimées jusqu'à environ 16 fois.
- Convention partagée avec wellness (T11) : toutes les lectures comptent pour le CPU et le trafic réseau,
  comme avant ; les blocs de lecture disque ne portent que sur les lectures qui trouvent un
  enregistrement : lectures × part trouvée × (1 − cache) × ⌈taille stockée ÷ 4 Kio⌉ (amendé le
  2026-11-19, T18 : et seulement quand le disque porte des enregistrements d'au moins un octet). Le cache
  (`cache_read_pct` mesuré, T16) se rapporte aux lectures qui atteignent le stockage, donc aux lectures
  trouvées : les deux parts se multiplient. L'IO de l'index sur flash (T9) reste d'un bloc par lecture,
  trouvée ou non : une lecture sans enregistrement cherche quand même son entrée.
- Nouveau champ de la charge `workload.reads_found` : part des lectures qui trouvent un enregistrement,
  en fraction de 0 à 1, bornée à [0, 1] comme les autres parts ; absent ou `null`, il vaut 1, si bien
  que les appelants et les liens antérieurs gardent leurs chiffres. La réponse renvoie la part retenue
  (`workload.reads_found` normalisé). Même nom en paramètre d'URL (T10), que wellness préremplit avec la
  part mesurée sur le cluster. Le formulaire l'affiche en %, au dixième, dans la section Workload, avec
  le moteur device seulement (sans lui, aucune lecture n'atteint de disque) ; l'explication et la carte
  IOPS citent la part quand elle est sous 100 %.
- Effet sur l'exemple par défaut : aucun (100 %). À 37 % de lectures trouvées : lectures 260 000 →
  96 200 IOPS, total 293 800 → 130 000 ; CPU, trafic et nœuds inchangés (le CPU dimensionne).
- wellness transmet désormais comme RF les **copies stockées** quand le facteur configuré les dépasse
  (`replication-factor 99` sur 2 à 4 nœuds stocke une copie par nœud) : min(facteur configuré, plus
  grand `effective_replication_factor` que rapportent les nœuds, quand il est supérieur à 0), avec une
  note qui nomme les namespaces concernés (sa décision T31, amendée le 2026-11-16, T44). Aucun
  changement de code ici : le sizer dimensionne les copies qu'on lui donne (RF borné à 8, plancher RF + 1
  nœuds ; amendé le 2026-11-19, T18 : 0 lu 1). Au-delà de 8 copies stockées (`replication-factor 99`
  sur 9 nœuds ou plus), wellness les transmet toutes et les dimensionne toutes ; le sizer en planifie 8,
  donc moins de nœuds, et une note d'avertissement de wellness le dit (ses décisions T29 et T31)
  (amendé le 2026-11-19, T18 : le sizer le dit aussi, dans `clamps` et sur sa page).

## T18 — Parité avec wellness : écarts relevés le 2026-11-19

Revue croisée des deux outils (2026-11-19), choix de l'utilisateur du même jour : chaque groupe d'écarts
a sa section ci-dessous ; wellness consigne les siens dans sa décision T46.

### Bornes jamais silencieuses, lectures sans octets d'enregistrement, retour vers wellness depuis toute adresse loopback

- **Bornes dites.** Chaque nombre que la normalisation ramène dans ses bornes (charge, nœud générique,
  hypothèses, réseau) revient dans `clamps` de `POST /api/plan` : `{field, given, used, note}`, où
  `field` est son chemin dans la requête (`workload.object_size`, `node.vcpu`, `hypotheses.growth`,
  `network.azs`, `network.rates.aws`…), `given` la valeur envoyée, `used` celle retenue et `note` une
  phrase en anglais pour la page. La liste est toujours présente, vide quand rien n'est borné, dans
  l'ordre charge, nœud, hypothèses, réseau ; `notes` garde son sens (les notes de bornes n'y sont pas
  ajoutées). Le mécanisme est unique (`sizing.Bounds`, `Bounded()` de chaque partie, dont `Normalize`
  est l'enveloppe) : toute borne nouvelle y passe.
  - Textes : propres aux bornes du modèle, qui disent ce que l'estimation laisse de côté — taille
    au-delà de 8 Mio (le plus grand enregistrement qu'Aerospike stocke : octets d'enregistrements et IO
    disque N fois moindres ; des enregistrements fusionnés de 93,94 Mio sont dimensionnés à 8 Mio,
    11,7 fois moins), RF au-delà de 8 (moins de copies, plancher de 9 nœuds au lieu de 11 pour un RF 10),
    entrées d'index secondaire au-delà de 10⁹ ; générique ailleurs (« Storage fill 1% is below 5%, the
    least the sizer accepts: sized with 5%. »), avec le libellé du champ sur la page.
  - Ne sont pas des bornes : les replis d'énumérations (moteur, placement d'index, perte, mode), les
    options que leurs prérequis excluent, une part de lectures trouvées absente (toutes les lectures),
    et le nombre de nœuds d'une ligne (le tableau montre celui retenu).
  - La page affiche les notes sous l'avis de pré-remplissage. Un RF au-delà de 8 prérempli par l'URL
    garde son bouton synthétisé (T14), accompagné de la note du serveur : le formulaire montre ce qui a
    été donné, la note ce qui a été retenu, comme pour tout autre champ.
  - wellness lit `field`, `given` et `used`, jamais `note`, et écrit ses propres textes en français et en
    anglais dans son panneau ; il saute `workload.rf`, déjà dit par sa note sur les copies au-delà de 8
    (T17). Les textes du sizer qui atteignent wellness restent en anglais à ce stade.
- **Entrées d'index secondaire : borne de 10⁴ à 10⁹ par enregistrement.** wellness garde les octets des
  index secondaires et de set quand il fusionne des objets (sa T26), et un cluster de peu d'objets porte
  ses octets d'index sur peu d'enregistrements : tous deux arrivent ici comme beaucoup d'entrées par
  enregistrement. 10⁴ ramenait l'exemple relevé à 40 % de sa RAM d'index secondaire (4 nœuds génériques
  au lieu de 8). 10⁹ dépasse de loin ce qu'un enregistrement de 8 Mio contient (quelques millions
  d'éléments de liste ou de map) : la borne arrête encore une valeur mal saisie, et les chiffres restent
  finis (au plus 8,5 × 10³⁴ o).
- **Lectures sans octets d'enregistrement.** Règle partagée avec wellness (son `ReadsReachDrives`) : une
  lecture ne lit des blocs d'enregistrement que sur le moteur device, quand il y a des enregistrements
  (`objects` > 0) et que leur taille stockée ((taille + surcoût) × compression) dépasse 0. Sinon elle ne
  coûte que du CPU et du réseau ; l'IO de l'index sur flash (T9) ne change pas. Un surcoût
  d'enregistrement donne des octets aux enregistrements, qui sont de nouveau lus.
- **Plafonds du formulaire de 5 à 100 %** (stockage, RAM, CPU, IO), comme le serveur : ils s'arrêtaient à
  10 %, si bien qu'un remplissage de 7 % saisi sur la page était dimensionné à 10 %. Un test lit chaque
  champ borné de la page et vérifie que le serveur borne juste au-delà et pas à la borne, hors des
  planchers propres au formulaire (taille d'enregistrement, ressources du nœud générique).
- **Lien retour vérifié par le serveur.** `back` (T10) est accepté s'il est en http, sans identifiants,
  vers localhost ou une adresse loopback (127.0.0.0/8, ::1, IPv4 mappée), les adresses que
  `WELLNESS_ADDR` accepte ; il est passé à la page dans `<body data-back>`. Sinon l'avis de
  pré-remplissage dit qu'il n'y a pas de lien retour, au lieu de se taire. wellness nomme sa page par
  l'hôte qu'emploie le navigateur : sur [::1] ou 127.0.0.2, le lien disparaissait.
- Compatibilité : requête inchangée ; `clamps` est un champ nouveau de la réponse, qu'un wellness plus
  ancien ignore. Pour la parité des chiffres sur des besoins sans enregistrement maître (tombstones
  seules), déployer le sizer d'abord.
- Effets :
  - exemple par défaut inchangé (aucune borne, 6 nœuds, 293 800 IOPS) ;
  - 10⁶ enregistrements de 0 o sur device à 100 000 lectures/s : 130 000 IOPS → 0, i4i.large 11 → 9 nœuds
    (CPU), L8s_v3 4 → 3 (topologie), comme wellness ;
  - entrées d'index secondaire au-delà de 10⁴ : dimensionnées entières ; l'exemple relevé, limité par la
    RAM, passe de 4 nœuds génériques (i4i.2xlarge × 7, 4 068 $/mois) à 8 (× 16, 9 297 $/mois), comme les
    besoins de wellness ;
  - un plafond de 5 à 10 % saisi sur la page est dimensionné tel quel (stockage à 7 % : 9 nœuds au lieu
    de 6, comme l'API et wellness) ;
  - le lien « Back to … » apparaît pour wellness sur [::1] ou 127.0.0.x ;
  - les notes de bornes ne changent aucun chiffre.

### Arrondi par excès à l'erreur de calcul près, partagé avec wellness

- Au multiple exact de la capacité d'un nœud, le sizer comptait un nœud de plus que wellness. Le CPU se
  calcule en deux divisions, (tx/s ÷ tx par vCPU) ÷ (vCPU × plafond) : 1 008 000 ÷ 15 000 ÷ 9,6 =
  7,000000000000001, que `math.Ceil` portait à 8. Le stockage aussi : la taille moyenne que transmet
  wellness est remultipliée par le nombre d'objets, et 28,125 To deviennent 28 125 000 000 000,004 o.
  Exemples : 1 008 000 tx/s sur 16 vCPU sans croissance ni perte, 8 nœuds ici contre 7 (9 contre 8 en
  N+1) ; hypothèses par défaut avec 6 480 000 + 6 480 000 tx/s, 119 contre 118 ; 28,125 To sur
  i4i.4xlarge à 50 %, 16 contre 15. `Evaluate` jugeait de plus insuffisante une configuration pile à ses
  plafonds (« Not enough CPU: 3.6 vCPU needed vs 3.6 vCPU usable »). wellness n'était pas exempt :
  553 846,15 tx/s mesurés, +30 %, y donnaient 720 000,0000000001 tx/s, donc 6 nœuds au lieu de 5, et
  16 vCPU au lieu de 8 dans son profil générique.
- Convention partagée (wellness T46) : tout arrondi par excès d'un ratio calculé passe par
  `ceilRatio(r)` = ⌈r × (1 − 10⁻¹²)⌉ (`sizing.RatioTolerance`) : les nœuds de chaque ressource, les blocs
  de 4 Kio d'une lecture (T12) et `partition-tree-sprigs` (T9). L'évaluation teste charge × (1 − 10⁻¹²) >
  limite (`exceeds`), pour 100 % comme pour le seuil « tight » de 85 %, et la jauge de l'interface fait
  de même avec `thresholds.tolerance` de `/api/defaults`.
- Pourquoi une tolérance plutôt qu'un réordonnancement des divisions : une seule division pour le CPU
  n'aurait corrigé ni le stockage (taille moyenne × nombre d'objets), ni les moyennes d'index et
  d'entrées d'index secondaire que transmet wellness, ni la croissance appliquée dans un autre ordre
  (wellness : (lectures + écritures) × (1 + g) ; ici lectures × (1 + g) + écritures × (1 + g)), ni
  l'erreur propre à wellness. Valeur : sur 3 000 clusters aléatoires, les demandes des deux outils
  diffèrent d'au plus 4,4 × 10⁻¹⁶ en relatif (deux ulps) ; 10⁻¹² en est plus de 2 000 fois et ne
  représente qu'un octet par To de demande : aucun excès réel n'est absorbé. C'est aussi la marge que
  wellness prend déjà pour ses quantiles (`model.Histogram.QuantileFloor`).
- La borne `MaxNodes` se compare au ratio arrondi, avant la conversion en entier ; elle n'est pas
  changée. ⌈nœuds ÷ racks⌉ reste un arrondi exact d'entiers. Aucune note : l'écart absorbé est du bruit
  de calcul.
- Effet, aux multiples exacts seulement : les exemples donnent 7, 8, 118 et 15 dans les deux outils, et
  une configuration pile à ses plafonds est « tight » à 100 %. Sur 3 000 clusters tirés au multiple
  exact d'un nœud, le sizer s'écartait du compte exact 648 fois (CPU) et 249 fois (stockage), wellness
  52 et 49 fois, et les deux outils différaient 663 et 271 fois : 0 après. L'exemple par défaut est
  inchangé ; sur les besoins des rapports réels de wellness, aucun nombre de nœuds ne change.
  `/api/defaults` gagne `thresholds.tolerance`.

### Index transmis par wellness : octets par copie effectifs, placement d'un bloc, pmem en mémoire

- **Octets d'index primaire par copie effectifs.** wellness envoyait `hypotheses.index_bytes` =
  max(64, octets d'index en mémoire ÷ (objets maîtres × RF le plus élevé)). Quand les namespaces ne
  stockent pas tous autant de copies, le vrai chiffre par copie est sous 64 B : le plancher le relevait et
  le sizer surestimait la RAM d'index primaire (18 instantanés réels mêlant 2 et 3 copies, de 39,4 à
  56,6 B par copie au RF 3 : de 13 à 62 % de trop). wellness envoie désormais tous les octets de l'index
  primaire (mémoire, pmem et minorité sur flash), mesurés ou estimés, ÷ (objets maîtres × RF), sans
  plancher et avec ses décimales : sous 64 B avec des copies mêlées, au-dessus avec des tombstones. La
  RAM d'index du sizer, objets × (1 + croissance) × RF × `index_bytes`, égale alors celle de wellness. Il
  envoie 64 quand l'index part sur flash ou sans enregistrement.
  - Le calcul du sizer ne change pas : `index_bytes` reste borné à [0, 1 Mio], qui accepte déjà moins de
    64. Le formulaire accepte les décimales (`step="any"`, unité « B per record copy ») : un chiffre
    prérempli de 43,68 était invalide pour `step="1"`, et les flèches l'arrondissaient à l'octet. Un
    chiffre effectif sous 64 allume le badge « what-if » ; l'unité, cette décision et la note de wellness
    sur les facteurs de réplication l'expliquent.
  - Exemple : 2 × 10⁹ objets en RF 2 et 10⁸ en RF 3, de 1 000 o, envoyés en RF 3 : 43,68 B par copie au
    lieu de 64, index primaire de 5,24 × 10¹¹ à 3,58 × 10¹¹ o ; la RAM dimensionne, i4i.4xlarge 9 → 6
    nœuds, i4i.xlarge 27 → 21, L8s_v3 15 → 12.
- **Chaque index placé d'un bloc.** Le sizer place l'index primaire tout en RAM ou tout sur flash, et de
  même les index secondaires. wellness l'envoie là où sont la plupart de ses octets, pmem compté en
  mémoire, jamais sur flash avec le moteur mémoire, et ses notes disent les octets déplacés.
  - Le sizer dit dans `notes` qu'il planifie en RAM un index demandé sur flash avec le moteur mémoire
    (« The primary index is planned in RAM: index-type flash does not work with the memory engine
    (configuration reference, 7.0). ») : le refus de T14 était silencieux. C'est la réécriture d'un
    placement, pas une borne : elle n'est pas dans `clamps`.
  - Un `index_bytes` au-delà de 1 Mio est une borne, dite dans `clamps` avec sa phrase (« Primary index:
    2,000,000 B per record copy asked, 1,048,576 B planned, the most an estimate accepts. ») ; le plus
    grand chiffre réel par copie est de 765,6 B.
- **pmem planifié en mémoire.** Aucune instance du catalogue n'a de pmem : un index en pmem (`index-type
  pmem`, `sindex-type pmem`) est planifié en RAM (`ram`) dans les deux outils, le placement le plus
  proche (mêmes entrées, sans IO disque). wellness compte les budgets des montages pmem d'index des nœuds
  actuels dans la RAM du nœud générique qu'il envoie (`node.ram_gib`) et plus dans son stockage
  (`node.storage_tb`), avec une note (choix de l'utilisateur).
- **Besoins sans enregistrement maître** (des tombstones seules, par exemple) : le sizer planifie les
  index et les enregistrements par objet maître et ne peut pas les représenter ; wellness lui envoie
  `objects` 0, le sizer ne planifie alors ni index ni stockage d'enregistrements, et wellness avertit
  que ses configurations laissent de côté ces octets d'index et d'enregistrements (choix de
  l'utilisateur : pas de champ de tombstones dans l'API ; wellness T46). Aucun cluster réel n'est dans
  ce cas. (amendé le 2026-11-19, T20 : avec l'index sur flash, le sizer planifie toutefois 256 sprigs
  par partition, le minimum configurable, et wellness les mêmes.)
- **Écart qui demeure** : l'espace de l'index primaire all-flash. wellness compte les octets mesurés
  (`index_flash_used_bytes`, environ 64 B par entrée) ; le sizer compte les blocs de sprigs du guide (T9 :
  4 096 partitions × RF × sprigs à 50 % de remplissage, arrondis à une puissance de deux, et 10 B de RAM
  par sprig) et l'IO de l'index. Sur les 17 instantanés réels à index flash, l'index flash du sizer fait
  2,2 à 3,8 fois celui de wellness : +13 à +38 % de stockage, jusqu'à 687 Go de RAM de sprigs. Hors de
  ce tour, à décider. (amendé le 2026-11-19, T20 : écart fermé, wellness planifie l'index sur flash comme
  le sizer, sprigs, RAM des sprigs et IO d'index compris.)
- Compatibilité : aucun champ ajouté ni retiré à la requête, à la réponse ou au pré-remplissage ; un
  appelant autre que wellness garde chacun de ses chiffres et le sens de chaque champ.
- Effets : aucun chiffre ne change pour une requête donnée ; les deux phrases n'apparaissent qu'avec un
  index sur flash et le moteur mémoire, ou plus de 1 Mio par copie. Côté wellness, seuls changent les
  18 instantanés à copies mêlées : la RAM d'index que le sizer leur planifie baisse de 12 à 38 % et égale
  celle de wellness, avec moins de nœuds là où la RAM dimensionne ; les 28 instantanés à un seul facteur
  envoient entre 63,9 et 64 B au lieu de 64, moins de 0,1 % d'écart.

### Conventions partagées consignées, liste commune des deux CLAUDE.md

- wellness s'appuie sur des conventions du sizer que ses décisions ne disaient pas. Elles sont consignées
  ici, sans changement de code :
  - un `rf` explicite de 0 ou moins est lu 1 (plancher de 2 nœuds ; borne dite dans `clamps`), un `rf`
    absent garde le défaut du formulaire (2), au-delà de 8 : 8 ;
  - gabarit inadapté quand une ressource demande plus de 10 000 nœuds en service (avant le nœud perdu et
    l'arrondi aux racks, `MaxNodes`) : 0 nœud et la ressource qui manque, aucune recommandation sur ce
    gabarit ni configuration confortable au-delà ; nœuds d'une configuration ajoutée bornés de 1 à
    10 000 ;
  - bornes des hypothèses, basses comme hautes : plafonds (stockage, RAM, CPU, IO), compression et
    remplissage des sprigs de 5 à 100 %, cache 0 à 100 %, croissance 0 à +10 000 %, surcoût d'écriture
    0 à 10, défragmentation 1 à 100, 100 à 10 000 000 transactions par vCPU, réserve OS 0 à
    1 000 000 Gio et au plus 90 % de la RAM, octets d'index primaire par copie, de RAM par sprig et par
    entrée d'index secondaire 0 à 1 Mio, surcoût d'enregistrement 0 à 8 Mio, sparseness 1 à 100 ; perte
    inconnue lue un nœud ;
  - défauts communs : croissance 30 %, stockage 50 % sans surcoût, RAM 70 % après max(5 Gio, 10 %),
    CPU 60 % de 15 000 transactions/s par vCPU, IO 100 %, défragmentation 1,3, cache 0, perte d'un nœud ;
  - une sous-transaction batch compte comme une transaction dans les lectures et écritures par seconde
    (CPU, IOPS, réseau) ;
  - la perte d'un rack ne planifie jamais moins de nœuds que celle d'un nœud ;
  - un champ absent de la requête garde la valeur du formulaire d'ouverture (`DefaultRequest`) : wellness
    n'envoie qu'une partie des réglages réseau.
- La liste « Dimensionnement partagé » est la même dans les deux `CLAUDE.md`, avec les décisions de chaque
  outil (wellness T46) : celle du sizer omettait la perte à survivre, les copies stockées et les bornes,
  celle de wellness les plafonds, les blocs de 4 Kio, le cache nul et les IOPS de planification.
- Les textes du sizer qui atteignent wellness (explication, adéquation, notes, bornes) restent en anglais
  à ce stade ; leur traduction est l'objet d'un tour suivant.
- Effet : aucun chiffre ne change.

### Notes de bornes près de la borne, liste partagée complétée

- **Valeur et borne jamais écrites de même.** Près d'une borne, le format de la page écrivait de même la
  valeur donnée et la valeur retenue : « Average record size 8.00 MiB is above 8.00 MiB » pour
  8 390 000 o, « Storage fill 100% is above 100% » pour 1,0004, « Records 1e+15 is above 1e+15 »,
  « Growth 10000% is above 10000% » ; et le rapport, au dixième, disait « 1 times below » jusqu'à 5 %
  au-delà de 8 Mio (8 700 000 o : 1,037 fois). Le champ « Average record size » de la page n'a pas de
  maximum, et wellness envoie des enregistrements fusionnés juste au-delà de 8 Mio.
  - Quand le format de la page les écrit de même, la note écrit la valeur donnée et la valeur retenue
    exactement, avec autant de décimales qu'il en faut pour les distinguer (« 8,390,000 B is above
    8,388,608 B », « 100.04% is above 100.00% », « 4.99% is below 5.00% »).
  - Un rapport sous 1,1 garde son écart, avec deux décimales au moins (« 1.04 times below »,
    « 1.0002 times below », « 1.03 times fewer than given »).
  - Les nombres non entiers prennent leurs séparateurs de milliers (« 1,048,576.5 B »). Ailleurs, les
    textes ne changent pas ; wellness ne lit pas `note`.
- **Liste partagée des `CLAUDE.md`.**
  - La règle de lecture de la première section rejoint la ligne des IO disque : aucun bloc sans
    enregistrement ou avec des enregistrements de 0 octet (moteur device, objets > 0, taille
    stockée > 0), avec sizer T18 et wellness T46. La ligne disait ⌈taille stockée ÷ 4 Kio⌉ blocs
    sur les lectures trouvées sans condition : 100 000 IOPS pour 0 objet de 1 024 o à 100 000
    lectures/s, où le sizer en compte 0.
  - La ligne des IOPS de planification prêtait au sizer les Azure Lasv3, que son catalogue n'a pas
    (L8s_v3 à L80s_v3 seulement) : elle les attribue au seul catalogue de wellness, tant que le sizer
    ne les ajoute pas (amendé le 2026-11-19, T19 : le sizer les ajoute, la ligne les attribue aux deux
    catalogues).
  - Les deux modifications sont à reporter à l'identique dans la liste de wellness, qui reste la même
    dans les deux `CLAUDE.md`.
- Aucune décision antérieure n'est contredite. Effet : aucun chiffre ne change ; seules les notes de
  `clamps` près d'une borne changent de texte.

## T19 — Catalogue commun avec wellness, disponibilité par région, prix live AWS lus chez AWS

- Demande de l'utilisateur (2026-11-19, revue croisée des deux outils ; décision T47 de wellness) : pour
  un même cluster, les deux outils donnent les mêmes chiffres. Le sizer n'avait que 18 des 73 types de
  wellness (i4i jusqu'à 16xlarge, Z3, Lsv3) et wellness aucune des 14 C4A du sizer : un cluster sur C4A y
  était un matériel hors catalogue, sans prix, aux IOPS estimées (c4a-highmem-16-lssd : 400 000 au lieu
  de 150 000, 5 nœuds au lieu de 10), et le sizer ne pouvait ni recommander ni évaluer une i8g, une C3
  ou une L8as_v3 que wellness propose (une configuration ajoutée de ces types était ignorée comme
  inconnue).
- **Mêmes types.** Les deux catalogues listent les mêmes 87 types, avec les mêmes formes, IOPS de
  planification et prix de référence à la demande, relevés dans les mêmes régions (US East) : AWS i4i
  (large à 32xlarge), i7ie, i8g, i3en, r6id et im4gn (49) ; Google C4A, C3 et Z3 (26) ; Azure Lsv3 et
  Lasv3 (12). Le sizer gagne 55 types, wellness les 14 C4A.
- **IOPS de planification** : la moitié des IOPS d'écriture aléatoire publiées pour tout type AWS et
  Google (règle T13, étendue à AWS par T16), 60 000 par disque pour les Lsv3 et les Lasv3 (T16).
  Chiffres publiés, relus le 2026-11-19 : tables « instance store » d'AWS (stockage optimisé pour i4i,
  i7ie, i8g, i3en et im4gn, mémoire optimisée pour r6id) ; tables « Local SSD » de Google, Titanium SSD
  pour C4A et Z3, NVMe par nombre de disques pour C3 (90 000, 180 000, 360 000, 360 000, 800 000 et
  1 600 000 pour 1, 2, 4, 8, 16 et 32 disques). Les tests des deux outils tiennent la même table. Une
  IOPS de planification peut être fractionnaire, moitié d'un chiffre impair (i7ie.large 21 666,5 ;
  r6id.large 8 385,5).
- **Deux formats, un test de parité.** Le sizer garde son champ `iops` (chiffre de planification, sens
  inchangé dans l'API), wellness son `write_iops` (chiffre publié). Un test de chaque côté lit le
  `catalog.json` de l'autre dépôt, à côté dans `~/work` ou là où le nomme `WELLNESS_CATALOG` (ici) ou
  `SIZER_CATALOG` (chez wellness), et exige autant de types et, pour chacun, le même fournisseur, la
  même famille, la même architecture, la même forme, le même prix de référence et la même IOPS de
  planification ; il est sauté sans l'autre dépôt. Il lit la branche extraite de l'autre dépôt : un
  changement de catalogue se fait dans les deux avant que l'un ou l'autre `make ci` passe.
- **Prix de référence** : tous relus le 2026-11-19 (`PricesAsOf`) et inchangés : fichiers de prix d'AWS
  (us-east-1), API Retail Prices d'Azure (eastus, Linux à la demande), gcloud-compute.com (us-east1).
  Les Z3 prennent les prix au millionième de wellness (1,151931 $/h au lieu de 1,1519 ; moins de 0,03 $
  par mois et par nœud). Les C4A gardent les prix calculés de T13, à 0,0007 $/h au plus des prix publiés
  (c4a-standard-72-lssd : 4,2184 au lieu de 4,2191), le même chiffre dans les deux outils.
- **Famille, architecture, régions.** Chaque type porte `family`, `arch` (`arm64` pour i8g, im4gn et
  C4A, `x86_64` sinon) et `regions`, les régions de la liste du fournisseur qui le proposent : fichiers
  de prix d'AWS de chaque région, API Retail Prices d'Azure, tables de régions de gcloud-compute.com,
  lus type par type le 2026-11-19. Ne sont pas proposés : r6id en eu-west-3, ap-southeast-3 et
  sa-east-1 ; im4gn en ap-southeast-3 et sa-east-1 ; i8g.48xlarge en eu-central-1 ; C3 en
  asia-southeast2 ; Z3 en asia-southeast2 et southamerica-east1 ; Lasv3 en germanywestcentral. Les
  listes sont statiques et se relisent avec les prix ; la capacité par zone n'est pas modélisée.
- **Comportement par région.** La recommandation et l'alternative confortable ne prennent que les types
  proposés dans la région de la ligne (T5). Une configuration ajoutée d'un type que sa région ne propose
  pas est ignorée avec une note, comme un type inconnu (« AWS: instance type "r6id.xlarge" is not
  offered in eu-west-3 · Paris, ignored. ») ; la note d'un fournisseur sans type adapté nomme la région
  (« AWS: no instance type of the catalog offered in eu-west-3 · Paris suits this workload. »). Le
  formulaire d'ajout ne liste que les types de la région choisie, groupés par famille de la moins chère
  à la plus chère, et garde le type choisi quand la nouvelle région le propose ; la ligne d'une
  configuration et l'option d'un type Arm (AWS Graviton, Google Axion) finissent par « arm64 ».
- **Ordre du catalogue** : prix de référence, puis nom à prix égal (i4i.24xlarge avant i8g.24xlarge,
  toutes deux à 8,2368 $/h), pour une recommandation stable.
- **Prix live AWS** : les fichiers publics des pages de tarifs d'AWS
  (`b0.p.awsstatic.com/pricing/2.0/meteredUnitMaps/ec2/USD/current/ec2-ondemand-without-sec-sel/<lieu>/Linux/index.json`,
  un par région, tous les types qu'elle propose, Linux à la demande) remplacent ec2.shop (choix de
  l'utilisateur), qui n'a ni i7ie ni i8g en eu-west-1, eu-west-3 (région AWS par défaut), ap-southeast-1,
  ap-southeast-3 et sa-east-1, ni i8g en ap-northeast-1 : ces configurations seraient restées au prix de
  référence. Même correspondance exacte du type ; ce sont les fichiers d'où wellness tire ses prix (sa
  décision T23). Ils ne sont pas une API documentée : si leur format change, la recherche échoue et la
  ligne garde le prix de référence, avec un avertissement dans le journal ; un test fixe leur lecture.
  Exemple : i8g.xlarge en eu-west-3, 0,3982 $/h lu en direct au lieu des 0,3432 $/h de référence.
- **API** compatible, chaque changement est un champ ajouté : tout objet d'instance gagne `family`,
  `arch` et `regions` (`providers[].instances[]` de `/api/defaults`, `rows[].instance` et
  `rows[].alternative.instance` de `/api/plan`) ; `instance.iops` reste le chiffre de planification, la
  requête ne change pas. wellness n'envoie aucune ligne et ne reçoit donc jamais la note d'un type non
  proposé ; il lit désormais `instance.arch` (vide avec un sizer plus ancien). La région des lignes
  automatiques quand wellness appelle le sizer reste la région par défaut de chaque fournisseur (choix de
  l'utilisateur : un tour suivant aligne la règle de recommandation). Déployer le sizer d'abord, puis
  wellness ; les deux ordres sont sûrs.
- Effet sur l'exemple par défaut (prix de référence, calcul seul) : AWS i8g.xlarge × 12 (3 006 $/mois)
  au lieu de i4i.xlarge × 15 (3 756) ; Google c3-standard-22-lssd × 3 (2 788) au lieu de
  c4a-highmem-4-lssd × 12 (2 605 : la pénalité par nœud préfère 3 nœuds) ; Azure L8as_v3 × 9 (4 100) au
  lieu de L8s_v3 × 9 (4 573). Nœud générique (6) et notes inchangés. Une ligne tight de 3 × i4i.2xlarge
  en eu-west-3 reçoit l'alternative i3en.3xlarge, moins chère que i4i.4xlarge.
- Effet sur les 294 clusters réels analysés par wellness (les requêtes qu'il envoie, rejouées dans les
  deux versions, prix de référence, chiffres agrégés) : la configuration AWS change de type pour 215
  (famille i8g pour 142, i3en 19, i7ie 8 ; i4i reste la famille de 125), calcul total −15,2 % ; Azure
  passe aux Lasv3 de même forme pour les 294 (−10,3 %) ; Google change pour 41 (C3 au lieu de C4A,
  +1,5 % de calcul, la pénalité par nœud). Le sizer et wellness proposent désormais le même type pour
  28 clusters sur AWS (6 avant), 98 sur Google (18) et 216 sur Azure (0) : le reste tient aux règles de
  choix (wellness classe au prix 1 an en US East, sans la marge de 85 % ni la pénalité par nœud du
  sizer ; le sizer recommande dans sa région par défaut), pas au catalogue.

## T20 — Index primaire sur flash planifié de même par wellness (sprigs, RAM des sprigs, IO d'index)

- Choix de l'utilisateur (2026-11-19), après comparaison des deux outils (décision T48 de wellness), qui
  ferme l'écart laissé ouvert par T18. wellness comptait l'index primaire sur flash à ses octets mesurés
  (`index_used_bytes`, `index_flash_used_bytes` avant 7.0, environ 64 o par entrée), sans RAM de sprigs ni IO d'index, et notait l'IO
  comme un écart (ses décisions T44 et T46). Sur les 24 instantanés réels avec `index-type flash`
  (dernier instantané de chaque cluster analysé), l'index flash du sizer valait 2,2 à 3,8 fois celui de
  wellness, soit +13 à +38 % de stockage, plus 0,67 à 687 Go de RAM de sprigs et ×2,3 à ×14 d'IOPS ; le
  sizer comptait 1,08 à 1,44 fois les nœuds de wellness sur 1 258 des 1 752 gabarits comparés.
- **Convention partagée, celle de T9.**
  - `partition-tree-sprigs` = la plus petite puissance de deux ≥ ⌈enregistrements maîtres à l'horizon
    ÷ (64 × 0,5) ÷ 4 096⌉ (arrondi `ceilRatio`, T18), de 256 à 2²⁸, avec le renvoi au support Aerospike
    au-delà de 32 768.
  - Sur flash : 4 096 partitions × RF × sprigs × (4 096 + 5) o, compté dans le stockage.
  - RAM : 10 o par sprig.
  - IO : un bloc par lecture, trouvée ou non, plus une lecture et une écriture par copie écrite, au débit
    à l'horizon.
- **Rien ne change ici**, ni le calcul ni l'API. wellness envoie toujours la requête de T18 :
  `workload.index_on` `flash` quand la plupart des octets de l'index sont sur flash avec le moteur
  device, ou qu'aucun n'est en mémoire et qu'un namespace a `index-type flash` (jamais avec le moteur
  mémoire ; wellness T48, relecture), `workload.objects` = objets maîtres, `workload.rf` = copies stockées, `hypotheses.index_bytes`
  64 (sans usage sur flash), `hypotheses.sprig_fill` 0,5 et `hypotheses.sprig_ram_bytes` 10, qui sont
  désormais les constantes avec lesquelles il dimensionne lui-même. Le sizer applique la croissance aux
  objets comme wellness.
- **Garantie nouvelle.** Quand tous les namespaces, ceux sans enregistrement compris, placent leur
  index primaire de la même façon (pmem compté en mémoire, T18),
  `generic.demand.flash_index`, `sprigs`, `index_iops` et `sprigs_per_partition` du sizer égalent ceux
  que wellness planifie, et les deux outils donnent le même nombre de nœuds. Un namespace sans
  enregistrement compte : wellness planifie les sprigs de tout namespace en `index-type flash`, 256 par
  partition au moins, et `workload.rf` porte les copies du namespace qui en stocke le plus, quel que soit
  son index. Sans enregistrement maître, des tombstones seules sur flash donnent les mêmes 256 sprigs par
  partition dans les deux outils ; leurs autres octets restent l'écart de T18. Écarts qui demeurent :
  - placements mêlés entre namespaces qui ont des enregistrements, où le sizer place l'index d'un bloc
    (notes de T18) ;
  - un namespace en `index-type flash` sans enregistrement ni tombstone à côté de namespaces qui ont
    leur index en mémoire : le sizer, qui reçoit l'index d'un bloc en mémoire, où sont tous ses octets
    mesurés (T18), ne lui planifie rien ; wellness lui planifie 256 sprigs par partition (8,6 Go de
    blocs et 21 Mo de RAM en RF 2), et une note d'information de wellness le dit (T48, relecture du
    2026-11-19). Sans aucun octet d'index en mémoire, wellness envoie l'index sur flash et les deux
    outils planifient les mêmes 256 sprigs et la même IO d'index ;
  - un namespace sans enregistrement dont l'index est en mémoire et qui stocke plus de copies que ceux
    sur flash : le sizer planifie les sprigs et l'IO d'index à ces copies (`workload.rf`), wellness à
    celles des namespaces sur flash (les deux tiers des blocs pour 2 copies contre 3) ; la note de
    wellness sur les facteurs de réplication le dit ;
  - moteur mémoire avec `index-type flash`, que le sizer planifie en RAM (T18) ;
  - plus de 8 copies stockées (T17, T18).
- **La formule est tenue en deux copies.** Le même tableau de vecteurs figure dans les deux suites
  (`TestFlashIndexVectorsSharedWithWellness` ici, `TestFlashIndexVectorsSharedWithTheSizer` chez
  wellness), dont l'exemple du guide : 4 milliards d'enregistrements RF2 → 32 768 sprigs par partition,
  1 Tio sur flash, 2,5 Gio de RAM. Ses chiffres sont des entiers sous 2⁵³, comparés exactement. wellness
  se compare en plus au sizer déployé (`WELLNESS_SIZER_PARITY`), à lancer après le déploiement de l'un ou
  l'autre outil. Pas de module Go partagé, comme pour la tolérance d'arrondi (T18) : le modèle du sizer
  est interne (`internal/sizing`). Tout changement de la formule ou d'une constante se fait dans les deux
  dépôts et dans les deux tableaux.
- **Un seul namespace.** Le sizer planifie un seul namespace. wellness planifie les enregistrements
  maîtres de ses namespaces à index sur flash comme un seul namespace, au plus grand nombre de copies que
  l'un d'eux stocke (choix de l'utilisateur) : c'est ce que le sizer reçoit et planifie dans le cas de
  la garantie ci-dessus. Aerospike règle `partition-tree-sprigs` par namespace : sur les 16 instantanés
  réels à plusieurs namespaces sur flash, un calcul par namespace donnerait de −13,8 à +10,6 % de
  stockage, selon le côté où tombe l'arrondi à la puissance de deux. Les tombstones ne sont pas
  comptées : sur les instantanés réels, qui en portent jusqu'à 25 % des enregistrements maîtres, elles
  ne changeraient aucune puissance de deux.
- La liste « Dimensionnement partagé » des deux `CLAUDE.md` gagne la ligne de l'index primaire sur flash
  (sizer T9, T20 ; wellness T48).
- Effet : aucun chiffre du sizer ne change, pour aucune requête. Sur les 24 instantanés, la demande
  (stockage, RAM, IOPS, à six chiffres significatifs) et le nombre de nœuds de chaque gabarit du catalogue
  envoyé comme nœud générique sont désormais les mêmes dans les deux outils : 0 nombre de nœuds différent
  sur 1 752 comparaisons, contre 1 258 avant. Sur le matériel actuel connu où le stockage dimensionne,
  wellness rejoint le nombre de nœuds du sizer (+27 % chez wellness). Compatibilité : aucun champ ajouté,
  retiré ou renommé à la requête, à la réponse ou au pré-remplissage ; les deux ordres de déploiement sont
  sûrs.
