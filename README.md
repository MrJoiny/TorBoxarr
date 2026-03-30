# TorBoxarr

Pretends to be qBittorrent or SABnzbd so your *arr apps can use [TorBox](https://torbox.app) as a download backend.

When Sonarr or Radarr sends a torrent or NZB, TorBoxarr accepts it through the standard qBittorrent/SABnzbd API, submits it to TorBox, polls until the remote download finishes, pulls the files down locally, and places them where the *arr app expects to find them.

## How it works

TorBoxarr runs a single Go binary with an HTTP server and a set of background workers. The server handles two API surfaces:

- **/api/v2/...** implements enough of the qBittorrent Web API for Sonarr/Radarr torrent integration (login, add, info, delete, categories, transfer info)
- **/api** and **/sabnzbd/api** implement enough of the SABnzbd API for usenet integration (addfile, addurl, queue, history, delete)

Behind the API, a worker pipeline processes each download through a series of states:

```
accepted -> submit_pending -> remote_queued/remote_active -> local_download_pending
-> local_downloading -> local_verify -> completed -> remove_pending -> removed
```

Each stage has its own worker loop:

1. **Submitter** sends the torrent/NZB to TorBox's API. Retries with exponential backoff on transient failures.
2. **Poller** checks TorBox for download progress. Handles the queued-to-active transition, including recovery if the queue ID disappears before an active ID appears.
3. **Downloader** fetches completed files from TorBox to local staging. Supports resumable HTTP range downloads and checkpoints progress to SQLite periodically.
4. **Finalizer** verifies all parts are on disk, then moves the staging directory to the completed path.
5. **Remover** cleans up local files and deletes the remote task on TorBox when the *arr app sends a delete request.
6. **Pruner** periodically deletes old removed job records and expired qBit session tokens.

State is stored in a local SQLite database. The schema is managed with [goose](https://github.com/pressly/goose) migrations embedded in the binary.

## Setup

### Requirements

- Go 1.26+ (for building from source)
- A TorBox account with an API token
- Sonarr, Radarr, or a similar *arr application

### Configuration

TorBoxarr reads its config from environment variables. Copy the example file and fill in your credentials:

```bash
cp .env.example .env
```

Required variables:

| Variable | What it is |
|---|---|
| `TORBOXARR_TORBOX_API_TOKEN` | Your TorBox API token |
| `TORBOXARR_QBIT_PASSWORD` | Password for the fake qBittorrent login (you pick this, then use it in Sonarr/Radarr) |
| `TORBOXARR_SAB_API_KEY` | API key for the fake SABnzbd interface (same idea, you pick it) |

Optional overrides:

| Variable | Default | What it does |
|---|---|---|
| `TORBOXARR_SERVER_BASE_URL` | `http://localhost:8085` | Base URL the server reports to clients |
| `TORBOXARR_DATA_ROOT` | `/data` | Root directory for staging, completed files, and payloads |
| `TORBOXARR_DATABASE_PATH` | `/var/lib/torboxarr/torboxarr.db` | SQLite database path; defaults to a dedicated app-data directory |
| `TORBOXARR_LOG_LEVEL` | `INFO` | Log verbosity: DEBUG, INFO, WARN, or ERROR |

### Connecting Sonarr/Radarr

For torrent downloads, add a qBittorrent download client in Sonarr/Radarr:

- Host: wherever TorBoxarr is running
- Port: `8085`
- Username: `admin`
- Password: whatever you set in `TORBOXARR_QBIT_PASSWORD`

For usenet, add a SABnzbd download client:

- Host/port: same as above
- API key: whatever you set in `TORBOXARR_SAB_API_KEY`

## Running

### Docker (recommended)

```bash
docker compose -f deploy/docker-compose.yml up -d
```

The compose file bind-mounts `../data` into `/data` for downloads and payloads, stores the SQLite database in a dedicated internal app-data directory at `/var/lib/torboxarr/torboxarr.db`, and reads `../.env` for configuration. The container uses a distroless base image.

### From source

```bash
go build -o bin/torboxarr ./cmd/torboxarr
./bin/torboxarr
```

Direct binary runs default to `/var/lib/torboxarr/torboxarr.db`, which is Docker-first. If you want a different path for local development, set `TORBOXARR_DATABASE_PATH`.

The binary runs database migrations automatically on startup, so you don't need to run goose separately unless you want to manage migrations by hand:

```bash
goose -dir internal/store/migrations sqlite3 /var/lib/torboxarr/torboxarr.db up
```

## Development

### Format

```bash
gofmt -w ./cmd ./internal
```

### Test

```bash
go test -count=1 ./...
```

### Test coverage

```bash
go test -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
```

### Build

```bash
go build -o bin/torboxarr ./cmd/torboxarr
```

## Project structure

```
cmd/torboxarr/       Entry point, wiring, server startup
internal/
  api/               HTTP handlers for qBit and SABnzbd APIs
  auth/              Session management (qBit cookies, SAB API keys)
  compat/            Translates internal job state to qBit/SAB response formats
  config/            Environment variable parsing and validation
  files/             Filesystem layout, range downloader, staging/completed paths
  store/             SQLite database, job and transfer part persistence, migrations
  torbox/            TorBox API client with rate limiting
  worker/            Background worker loops (submit, poll, download, finalize, remove, prune)
deploy/
  Dockerfile         Multi-stage build, distroless runtime
  docker-compose.yml Minimal compose setup, bind-mounting ../data
```
