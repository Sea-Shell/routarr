# AGENTS.md

## What this repo is
`Routarr` — a Go web app that syncs YouTube playlists to Spotify. Runs as an HTTP server, authenticates via OAuth, and persists everything in a local SQLite file.

## Commands

```bash
make build   # go build -o bin/routarr ./cmd/routarr
make test    # go test -v ./...
make run     # build + ./bin/routarr
make clean   # rm -rf bin/
```

Run a single package:
```bash
go test -v ./internal/matcher/...
go test -v ./internal/adapters/sqlite/...
go test -run TestCreateMappingAndIndex ./internal/adapters/web/...
```

No linter config exists in the repo. `go vet ./...` is the only static check available.

## Environment variables

| Variable | Default | Notes |
|---|---|---|
| `ADDR` | `:8080` | Listen address |
| `DB_PATH` | `routarr.db` | SQLite file path |
| `YT_CLIENT_ID` | _(required for OAuth)_ | Google Cloud Console |
| `YT_SECRET` | _(required for OAuth)_ | Google Cloud Console |
| `SP_CLIENT_ID` | _(required for OAuth)_ | Spotify Developer Dashboard |
| `SP_SECRET` | _(required for OAuth)_ | Spotify Developer Dashboard |

The `.env` file in the repo root contains only the defaults (`ADDR=:8080`, `DB_PATH=routarr.db`) — it is **not** auto-loaded. Export variables manually or use a tool like `dotenv`. OAuth credentials are not committed.

OAuth callback URLs are hardcoded to `http://localhost:8080/oauth/youtube/callback` and `http://localhost:8080/oauth/spotify/callback`. Your OAuth app registrations must use these exact redirect URIs.

## Architecture

Hexagonal (ports & adapters):

```
cmd/routarr/main.go          — wires everything together
internal/config/           — env-based config, no file loading
internal/domain/           — pure value types (PlaylistMapping, SyncRun, TrackMatch, MatchDecision)
internal/ports/            — interfaces: MappingRepository, MatchRepository, YouTubeService, SpotifyService
internal/app/sync_service.go — orchestrates a sync run; no HTTP, no DB calls directly
internal/matcher/          — F1-score title matcher (score > 0.8 = auto, < 0.4 = rejected, else pending)
internal/adapters/sqlite/  — implements ports + manages migrations
internal/adapters/youtube/ — calls YouTube Data API v3
internal/adapters/spotify/ — calls Spotify Web API
internal/adapters/web/     — HTTP handlers + HTML templates (embedded via go:embed)
```

## Key quirks

**Migrations run automatically.** `sqlite.Open()` calls `RunMigrations()` before returning. There is no separate migrate command.

**SQLite uses `modernc.org/sqlite` (pure Go, no CGO).** Do not add a CGO-dependent SQLite driver.

**HTML templates are embedded at compile time.** Editing `.gohtml` files under `internal/adapters/web/templates/` requires a rebuild to take effect; there is no hot-reload.

**`SyncService` acquires its `syncRunRepo` via interface type-assertion on `mappingRepo`.** If you pass a repository that doesn't implement the private `syncRunRepository` interface, sync runs will not be persisted. The concrete `sqlite.Repository` implements all required interfaces.

**`adapter.baseURL` is an exported field in both `youtube` and `spotify` adapters.** Tests override it to point at an `httptest.Server` — this is the intended injection point for tests, not a method or constructor option.

**Web handler tests use a real in-process SQLite DB** (not mocked). The `web` package tests import `internal/adapters/sqlite` directly. There are no external services required to run any test.

**`YT_CLIENT_ID` and `YT_SECRET` are trimmed with `strings.TrimSpace`; `SP_CLIENT_ID`/`SP_SECRET` are not.** Trailing whitespace in Spotify creds will cause auth failures.
