# yt2sp

A Go web app that syncs YouTube playlists to Spotify.  
Runs as an HTTP server, authenticates via OAuth, and persists everything in a local SQLite file.

---

## Table of contents

- [Quick start](#quick-start)
- [How it works](#how-it-works)
- [OAuth setup — Spotify](#oauth-setup--spotify)
- [OAuth setup — YouTube](#oauth-setup--youtube)
- [Environment variables](#environment-variables)
- [Local HTTPS development](#local-https-development)
- [Proxy support](#proxy-support)
- [Running the app](#running-the-app)
- [Architecture](#architecture)

---

## Quick start

```bash
git clone https://github.com/bateau84/yt2sp
cd yt2sp
make build
```

Set your credentials (see the two OAuth guides below), then:

```bash
export YT_CLIENT_ID=...
export YT_SECRET=...
export SP_CLIENT_ID=...
export SP_SECRET=...
make run
```

Open `http://localhost:8080` in your browser.

---

## How it works

1. **Connect** — visit the dashboard and connect your YouTube and Spotify accounts via OAuth. Tokens are stored in the local SQLite database and refreshed automatically.
2. **Create a mapping** — click **+ New Mapping**. Pick source and destination playlists from the dropdowns (populated from your accounts) or enter IDs manually. A display label is auto-filled from the playlist name and can be overridden.
3. **Dry sync** — click **Sync** on a mapping. The app fetches all YouTube videos, searches Spotify for each track, and scores matches using an F1 title matcher. Nothing is written to Spotify yet.
4. **Review** — tracks scoring above 0.8 are auto-approved; below 0.4 are rejected; everything in between is queued for manual review. The review UI lets you pick from up to five Spotify candidates per track, or reject it.
5. **Commit** — once satisfied with the matches, click **Commit**. Approved tracks are added to the Spotify destination playlist.

---

## OAuth setup — Spotify

### 1. Create a Spotify Developer app

1. Go to [https://developer.spotify.com/dashboard](https://developer.spotify.com/dashboard) and log in with your Spotify account.
2. Click **Create app**.
3. Fill in the form:
   - **App name** — any name, e.g. `yt2sp`
   - **App description** — e.g. `YouTube to Spotify sync`
   - **Website** — can be left blank or set to `http://localhost:8080`
   - **Redirect URI** — enter the URI that matches how you run the app:
      - Plain HTTP (default): `http://localhost:8080/oauth/spotify/callback`
      - HTTPS (when TLS is enabled — see [Local HTTPS development](#local-https-development)): `https://localhost:8080/oauth/spotify/callback`
   - **APIs used** — tick **Web API**
4. Accept the terms and click **Save**.

### 2. Get your credentials

1. On the app's dashboard page click **Settings**.
2. Copy the **Client ID** — this is your `SP_CLIENT_ID`.
3. Click **View client secret**, then copy it — this is your `SP_SECRET`.

### 3. Export the variables

```bash
export SP_CLIENT_ID=your_client_id_here
export SP_SECRET=your_client_secret_here
```

> **Tip:** If the Spotify OAuth page shows *"INVALID_CLIENT: Invalid redirect URI"*, double-check that the Redirect URI in the dashboard matches the URL you registered exactly — no trailing slash. When TLS is enabled the redirect URI must use `https://`; the `http://` variant will be rejected.

---

## OAuth setup — YouTube

### 1. Create a Google Cloud project

1. Go to [https://console.cloud.google.com](https://console.cloud.google.com) and log in with your Google account.
2. Click the project selector at the top of the page and choose **New project**.
3. Give it a name (e.g. `yt2sp`) and click **Create**.

### 2. Enable the YouTube Data API v3

1. In the left sidebar go to **APIs & Services → Library**.
2. Search for **YouTube Data API v3** and click on it.
3. Click **Enable**.

### 3. Configure the OAuth consent screen

1. Go to **APIs & Services → OAuth consent screen**.
2. Choose **External** as the user type and click **Create**.
3. Fill in the required fields:
   - **App name** — e.g. `yt2sp`
   - **User support email** — your Google account email
   - **Developer contact information** — your email again
4. Click **Save and Continue** through the **Scopes** and **Test users** screens (no changes needed).
5. On the **Test users** screen, click **+ Add users** and add your own Google account email. This allows you to authenticate while the app is in *Testing* mode.
6. Click **Save and Continue**, then **Back to Dashboard**.

### 4. Create OAuth credentials

1. Go to **APIs & Services → Credentials**.
2. Click **+ Create Credentials → OAuth client ID**.
3. Set **Application type** to **Web application**.
4. Give it a name, e.g. `yt2sp-local`.
5. Under **Authorized redirect URIs**, click **+ Add URI** and enter the URI that matches how you run the app:
   - Plain HTTP (default): `http://localhost:8080/oauth/youtube/callback`
   - HTTPS (when TLS is enabled — see [Local HTTPS development](#local-https-development)): `https://localhost:8080/oauth/youtube/callback`
6. Click **Create**.

### 5. Get your credentials

A dialog appears with your credentials:
- **Your Client ID** — this is your `YT_CLIENT_ID`.
- **Your Client Secret** — this is your `YT_SECRET`.

Copy both values. You can also download them as a JSON file for reference.

### 6. Export the variables

```bash
export YT_CLIENT_ID=your_client_id_here
export YT_SECRET=your_client_secret_here
```

> **Tip:** Google trims `YT_CLIENT_ID` and `YT_SECRET` automatically, but make sure there is no trailing whitespace when you paste them. Spotify credentials (`SP_CLIENT_ID`, `SP_SECRET`) are **not** trimmed — trailing whitespace will cause auth failures.

> **Tip:** If Google shows *"Access blocked: yt2sp has not completed the Google verification process"*, click **Advanced → Go to yt2sp (unsafe)**. This is expected for personal/testing apps that have not been submitted for review.

---

## Environment variables

| Variable | Default | Required | Notes |
|---|---|---|---|
| `ADDR` | `:8080` | No | Listen address |
| `DB_PATH` | `yt2sp.db` | No | SQLite file path |
| `YT_CLIENT_ID` | — | Yes | Google Cloud Console; leading/trailing whitespace is trimmed automatically |
| `YT_SECRET` | — | Yes | Google Cloud Console; leading/trailing whitespace is trimmed automatically |
| `SP_CLIENT_ID` | — | Yes | Spotify Developer Dashboard; whitespace is **not** trimmed — trailing spaces cause auth failures |
| `SP_SECRET` | — | Yes | Spotify Developer Dashboard; whitespace is **not** trimmed — trailing spaces cause auth failures |
| `TLS_CERT_FILE` | — | No | Path to a PEM TLS certificate. Set together with `TLS_KEY_FILE` to enable HTTPS. If the file does not exist the app generates a self-signed certificate at that path |
| `TLS_KEY_FILE` | — | No | Path to a PEM TLS private key. Must be set together with `TLS_CERT_FILE` |
| `OAUTH_BASE_URL` | — | No | Override the base URL used to build OAuth redirect URIs (e.g. `https://myapp.com`). Required when the app is behind a TLS-terminating proxy — see [Proxy support](#proxy-support) |

A `.env` file in the repo root sets the defaults for `ADDR` and `DB_PATH`. It is **not** auto-loaded — export variables manually or use a tool like `dotenv`.

---

## Local HTTPS development

Setting both `TLS_CERT_FILE` and `TLS_KEY_FILE` switches the server from HTTP to HTTPS.

```bash
export TLS_CERT_FILE=cert.pem
export TLS_KEY_FILE=key.pem
make run
```

**Self-signed certificate auto-generation:** if the files referenced by `TLS_CERT_FILE` and `TLS_KEY_FILE` do not exist on disk the app creates a self-signed certificate and writes it to those paths on startup. No external tool (e.g. `openssl`, `mkcert`) is required.

> **Browser warning:** self-signed certificates are not trusted by browsers. You will see a security warning the first time you open the app. Click **Advanced → Proceed** (or your browser's equivalent) to continue. This is expected for local development.

### Registering HTTPS callback URLs with OAuth providers

When HTTPS is enabled you must register the `https://` variants of the callback URLs in both provider dashboards — the `http://` variants will be rejected.

| Provider | Callback URL to register |
|---|---|
| YouTube / Google | `https://localhost:8080/oauth/youtube/callback` |
| Spotify | `https://localhost:8080/oauth/spotify/callback` |

If you changed `ADDR` to a port other than `8080`, adjust the port in the URLs above accordingly.

---

## Proxy support

When the app runs behind a TLS-terminating reverse proxy (nginx, Caddy, Traefik, etc.) the app itself is contacted over plain HTTP, but the OAuth providers need to redirect back to the public-facing HTTPS URL.

Set `OAUTH_BASE_URL` to the public base URL of your deployment:

```bash
export OAUTH_BASE_URL=https://myapp.com
make run
```

The app will use this value as the scheme and host when constructing the OAuth redirect URIs sent to YouTube and Spotify, regardless of the address the server is actually listening on.

Register the corresponding callback URLs in your OAuth provider dashboards:

| Provider | Callback URL to register |
|---|---|
| YouTube / Google | `https://myapp.com/oauth/youtube/callback` |
| Spotify | `https://myapp.com/oauth/spotify/callback` |

> **Note:** `OAUTH_BASE_URL` and `TLS_CERT_FILE`/`TLS_KEY_FILE` are independent. You can run with direct TLS (no proxy) by setting the cert/key variables, behind a proxy by setting `OAUTH_BASE_URL`, or both together if your proxy also terminates TLS for an upstream HTTPS server.

---

## Running the app

```bash
# Build
make build

# Run (after exporting all four OAuth variables)
make run

# Or run the binary directly
./bin/yt2sp
```

The app starts at `http://localhost:8080`. On first run it will create `yt2sp.db` and apply all migrations automatically.

To run tests:

```bash
make test          # go test -v ./...
go vet ./...       # static analysis
```

---

## Architecture

Hexagonal (ports & adapters):

```
cmd/yt2sp/main.go            — wires everything together
internal/config/             — env-based config, no file loading
internal/domain/             — pure value types (PlaylistMapping, SyncRun, TrackMatch, MatchDecision)
internal/ports/              — interfaces: MappingRepository, MatchRepository, YouTubeService, SpotifyService
internal/app/sync_service.go — orchestrates a sync run; no HTTP, no DB calls directly
internal/matcher/            — F1-score title matcher (score > 0.8 = auto, < 0.4 = rejected, else pending)
internal/adapters/sqlite/    — implements ports; manages migrations, OAuth token storage, sync run events, track match candidates
internal/adapters/youtube/   — calls YouTube Data API v3; lists user playlists + fetches playlist videos
internal/adapters/spotify/   — calls Spotify Web API; lists user playlists, searches tracks, adds to playlist
internal/adapters/web/       — HTTP handlers + HTML templates (embedded via go:embed); OAuth flow, mapping CRUD, sync UI
internal/tlsutil/            — self-signed TLS certificate generation for local HTTPS development
```
