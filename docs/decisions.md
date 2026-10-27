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
  hypothèses reprennent celles de wellness (T22) : stockage 70 %, RAM 70 % après 5 Gio réservés à l'OS,
  CPU 60 % de 15 000 transactions/s par vCPU, IO 100 % des IOPS soutenues, N+1, nœuds multiples du
  nombre de racks. Toutes sont modifiables dans l'interface (badge « what-if »).
- Le CPU s'ajoute aux ressources (comme dans wellness) : sans lui, le comparateur recommandait des nœuds
  de 2 à 4 vCPU pour des centaines de milliers de transactions par seconde. Le nœud générique a un champ
  vCPU (16 par défaut).
- Croissance : appliquée aux données (stockage, RAM), pas au débit, comme le sizer d'origine.
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
