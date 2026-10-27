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
