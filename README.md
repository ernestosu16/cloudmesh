# CloudMesh

**English** · [Español](README.es.md)

Many clouds, one drive — RAID over cloud storage accounts. CloudMesh connects several storage accounts (Mega in v1) and turns them into a single self-hosted drive: pooled quota, virtual folders, and every file split into blocks spread across accounts with configurable Reed–Solomon parity (`k+m`), so losing up to `m` accounts loses no data. Every block leaves the server encrypted with AES-256-GCM and a per-file key: providers only ever see ciphertext.

Go + React 19 in a single container, SQLite, no external services.

> [!WARNING]
> CloudMesh is provided as is, without warranty of any kind. **Each person who uses it is solely responsible for how they use it**: for complying with the terms of service of every storage provider they connect, with the laws that apply to them, and for the data they store. Read the [disclaimer](#disclaimer) before deploying it.

![My space](docs/screenshots/drive.png)

| | |
|---|---|
| ![Storage pool](docs/screenshots/storage.png) | ![S3 buckets](docs/screenshots/s3.png) |
| **Storage**: connected accounts, pooled capacity and redundancy health | **S3 API**: buckets mapped to drive folders, R2-style access keys |
| ![Public shared folder](docs/screenshots/public-share.png) | ![Dark theme](docs/screenshots/drive-dark.png) |
| **Shared links**: read-only public pages, optional password, ZIP download | **Light and dark themes**, English and Spanish UI |
| ![Users and roles](docs/screenshots/users.png) | <img src="docs/screenshots/mobile-drive.png" alt="Mobile view" width="260"> |
| **Users**: roles, per-user quotas, invitations and 2FA | **Mobile-first**, installable as a web app |

The screenshots use demo data and the development `fake` provider.

## Features

- **One drive over many accounts**: their quotas add up; people using CloudMesh only see folders and files.
- **Erasure coding**: each file is cut into stripes of `k` data blocks plus `m` parity blocks (Reed–Solomon), each stored on a different account. Small files are stored as encrypted copies instead of stripes. Each file keeps its own `k+m`; changing settings only affects new uploads.
- **End-to-end block encryption**: AES-256-GCM, one key per file wrapped with your master key. Without that key the data at the providers is unreadable.
- **Self-healing**: every 15 minutes a maintenance pass syncs with the providers, verifies that every block is still there, rebuilds missing ones from parity and backs up the encrypted index to every account.
- **Disaster recovery**: rebuild the whole server from any surviving account plus the master key.
- **Streaming uploads and downloads** with `Range` support for video and audio previews, a small in-memory cache for viewers, and an upload queue that accepts whole folders.
- **Previews** for images, video, audio, PDF and text; search; rename and move; trash with 30-day retention.
- **Public shared links** to files or folders, read-only, optional password, download counter.
- **S3-compatible API** in the style of Cloudflare R2: SigV4, presigned URLs, multipart, public buckets, CORS and lifecycle rules. Works with `aws`, `rclone`, `restic` and friends.
- **Multi-user**: admin / editor / reader roles, per-user quotas, invitations by link or email, restricted folders, TOTP two-factor authentication with recovery codes.
- **Single static binary** with the frontend embedded, SQLite database, distroless image. Installable PWA, bilingual UI (English / Spanish), light and dark themes.

## How it works

```
browser / S3 client
        │
        ▼
  CloudMesh (Go)  ── SQLite index: users, folders, files, blocks
        │
        │  split into stripes · Reed–Solomon k+m · AES-256-GCM per block
        ▼
 ┌──────────┬──────────┬──────────┐
 │ account 1│ account 2│ account 3│   …   (Mega in v1)
 │  data    │  data    │  parity  │
 └──────────┴──────────┴──────────┘
```

With the default `2+1`, any one account can disappear (deleted, banned, out of transfer quota) and every file can still be read and repaired. Uploads are routed by free space (`most_free`, default), `round_robin` or `priority`. With fewer than `k+m` usable accounts a file is stored without parity (`1+0`) and the UI warns about it.

The full design, scope and phases are in [PLAN.md](PLAN.md) (Spanish).

## Install

You only need **Docker** (with Docker Compose). Go, Node and pnpm all run inside containers.

### Production on your own server

1. Clone the repository and create `.env` from the example:

   ```bash
   git clone https://github.com/ernestosu16/cloudmesh.git cloudmesh
   ```

   ```bash
   cd cloudmesh && cp .env.example .env
   ```

2. Generate the two required secrets and paste them into `.env`:

   ```bash
   openssl rand -base64 32
   ```

   ```
   CLOUDMESH_MASTER_KEY=<first value>
   CLOUDMESH_SESSION_SECRET=<second value>
   ```

   **Store `CLOUDMESH_MASTER_KEY` somewhere safe.** It encrypts the provider credentials and the keys of every file. If it is lost, the stored data cannot be decrypted and every account has to be reconnected.

3. Build and start the production image (app on port `8080`, S3 API on `9000`):

   ```bash
   make prod
   ```

   Without `make`:

   ```bash
   docker compose -f docker-compose.dokploy.yml -f docker-compose.prod-local.yml --env-file .env up --build -d
   ```

4. Find the one-time setup code in the logs, a line with `"setup_code":"XXXX-XXXX"`:

   ```bash
   docker compose -f docker-compose.dokploy.yml logs app
   ```

5. Open `http://your-server:8080`. It redirects to `/setup`: paste the code and create the administrator. If you lose the code, restart the container; a new one is generated as long as there are no users.

6. In **Storage → Add account**, connect your Mega accounts (email, password and 2FA code if enabled). Connect at least `k+m` accounts (three for the default `2+1`) to get redundancy.

Put CloudMesh behind a reverse proxy with HTTPS (Traefik, Caddy, nginx) for anything beyond a local network, and set `CLOUDMESH_TRUST_PROXY=1` so it reads `X-Forwarded-For` and `X-Forwarded-Proto`. All data lives in the `/data` volume.

### Production with Dokploy

The final image is a static binary on `distroless` with the frontend embedded, and persists everything in the `/data` volume (SQLite).

1. Push the repository to GitHub, GitLab or Gitea.
2. In Dokploy: **Create Service → Compose**. Provider: your repository, branch `main`, **Compose Path** `docker-compose.dokploy.yml`.
3. **Environment** tab: define `CLOUDMESH_MASTER_KEY` and `CLOUDMESH_SESSION_SECRET` (`openssl rand -base64 32` each). `CLOUDMESH_TRUST_PROXY` already defaults to `1` in that compose file.
4. **Domains** tab: add your domain pointing to service `app`, port `8080`, HTTPS with Let's Encrypt. For the S3 API, add another domain (e.g. `s3.yourdomain`) on port `9000` and set it in `CLOUDMESH_S3_URL`.
5. **Deploy**, then look for `setup_code` in **Logs**, open the domain and create the administrator.

The service gets the alias `cloudmesh-app` on `dokploy-network`, so another stack with an `app` service cannot capture its traffic.

#### Large uploads: the Traefik timeout

Traefik v3 (used by Dokploy) cuts any request whose body takes more than **60 seconds** to arrive (`respondingTimeouts.readTimeout`). A multi-GB upload from a home connection dies halfway with a 499 or 504 even though CloudMesh would accept it. Raise that limit in Dokploy → **Settings → Traefik → Traefik config**, on every entrypoint your domain uses (`web` and `websecure`):

```yaml
entryPoints:
  websecure:
    address: ":443"
    transport:
      respondingTimeouts:
        readTimeout: 4h
        writeTimeout: 4h
        idleTimeout: 180s
```

`4h` leaves room for 5 GiB at 3 Mbit/s. Save and restart Traefik. There is no need to change the body size: Traefik does not limit it unless you enable the `buffering` middleware. CloudMesh limits each file with `CLOUDMESH_MAX_UPLOAD_BYTES` and cuts a transfer that stays **2 minutes without moving data**; a slow but alive upload never expires.

Measured locally with the production image behind Traefik v3.6: a 1 GiB upload at 12 MB/s dies after 61 s with `499 Client Closed Request` on the default, and finishes in 85 s with `readTimeout: 4h`. You can reproduce it with `docker-compose.traefik-local.yml` and `TRAEFIK_READ_TIMEOUT=60s` vs `4h`.

### Memory sizing

Each upload in progress holds one stripe in memory (`k × block`, 64 MiB with `2+1` and 32 MiB blocks) plus parity and Go GC headroom: plan for about **250 MiB per upload** and **150 MiB per download** with the defaults. With `CLOUDMESH_MAX_UPLOADS=4` and `CLOUDMESH_MAX_DOWNLOADS=8` the worst case is around 2.5 GiB. On a 2 GiB VPS use `CLOUDMESH_MAX_UPLOADS=2`, `CLOUDMESH_MAX_DOWNLOADS=4` or 16 MiB blocks (Storage → Configure). CPU is not the bottleneck: parity runs at several GB/s.

## Configuration

Only the two secrets are required; everything else has a default. See [.env.example](.env.example).

| Variable | Default | Description |
|---|---|---|
| `CLOUDMESH_MASTER_KEY` | — | 32 bytes in base64. Encrypts provider credentials and file keys. **Required.** |
| `CLOUDMESH_SESSION_SECRET` | — | 32 bytes in base64. Signs session cookies. **Required.** |
| `CLOUDMESH_MAX_UPLOAD_BYTES` | `5368709120` | Maximum size per file (5 GiB). |
| `CLOUDMESH_MAX_UPLOADS` / `CLOUDMESH_MAX_DOWNLOADS` | `4` / `8` | Concurrent transfers per process. |
| `CLOUDMESH_TRUST_PROXY` | `0` | `1` behind a reverse proxy: trust `X-Forwarded-*`. |
| `CLOUDMESH_S3_ADDR` | `:9000` | S3 API listen address; `off` disables it. |
| `CLOUDMESH_S3_URL` | empty | Public S3 URL shown in the app; its host also enables virtual-host style. |
| `CLOUDMESH_S3_REGION` | `auto` | Advertised region. |
| `CLOUDMESH_SMTP_URL` / `CLOUDMESH_SMTP_FROM` | empty | `smtp://user:pass@host:587` or `smtps://…:465` to email invitations. |
| `CLOUDMESH_LOG_LEVEL` | `info` | `debug` logs per-block read timings. |

## Users and access

The first user is an administrator. From **Users** you invite others by link (or create them with a password) with a role — admin, editor or reader — and an optional storage quota. With `CLOUDMESH_SMTP_URL` the invitation is emailed. An administrator can restrict any folder to certain users (folder action **Access**). Enabling two-factor authentication shows ten single-use recovery codes.

## S3-compatible API

CloudMesh exposes an S3 API in the style of Cloudflare R2 on its own port (`CLOUDMESH_S3_ADDR`, default `:9000`).

- **Buckets** point to drive folders; `a/b/c.txt` is file `c.txt` in subfolders `a/b`. What you upload over S3 shows up in the drive, and existing files are readable over S3. Overwriting or deleting an object sends it to the trash.
- **Access keys** (S3 buckets page): Admin or Objects, read-only or read/write; object keys can be limited to some buckets. The secret is only shown once.
- **Per-bucket settings**: public access (anonymous GET/HEAD), CORS rules in R2's JSON format and lifecycle rules (expire objects under a prefix after N days, abort stale multipart uploads).
- **Protocol**: SigV4 (header, presigned URL and `aws-chunked`), path and virtual-host styles, region `auto`, ListObjects v1/v2, Get/Head with `Range`, Put, Copy, Delete and DeleteObjects, multipart with `UploadPartCopy`, `x-amz-meta-*` metadata. No versioning, ACLs, bucket policies, transitions or tags.

```bash
aws --endpoint-url https://s3.yourdomain s3 sync ./backups s3://backups
```

For rclone: `type = s3`, `provider = Other`, `endpoint = https://s3.yourdomain`, `region = auto`, `force_path_style = true` (or `false` with wildcard DNS `*.s3.yourdomain`).

## Encryption and Mega limits

- **Encryption**: file keys are wrapped with `CLOUDMESH_MASTER_KEY`. Without it nothing can be read, even with access to the accounts. Files that the sync finds already in a provider's `cloudmesh` folder are imported as they are (visible there, not encrypted); everything uploaded through CloudMesh is always stored as encrypted blocks.
- **Credentials**: the provider password is never stored, only a resumable session encrypted with the master key.
- **Transfer quota**: free Mega accounts limit how much can be downloaded. When one hits the limit, CloudMesh sets it aside for an hour, reads through the parity of the others and only uploads to it if there is no other option. A file without parity (`1+0`) on that account cannot be downloaded until Mega renews the quota.

## Backups and disaster recovery

Three layers, from most to least automatic:

- **Manifest in the clouds**: every 15 minutes, if the index changed, an encrypted copy (`cloudmesh/manifest.json.enc`) is stored in every account.
- **Local database copy**: every 24 hours the server writes `/data/backups/cloudmesh-<date>.db` (consistent `VACUUM INTO`) and keeps the last 7. Manually: `docker compose exec app /cloudmesh backup`.
- **Volume backup** of `/data` from your host or Dokploy.

`CLOUDMESH_MASTER_KEY` is in none of them. Keep it separately.

If you lose the whole server, with the same master key and one surviving account:

```bash
docker compose exec -i app /cloudmesh restore mega you@email.com
```

It asks for the Mega password (add the 2FA code as a third argument if you use it), downloads the manifest, decrypts it and rebuilds users, accounts, folders, files and blocks into the empty database. That account stays connected; reconnect the others from **Storage**. With an index exported from the app: `cloudmesh restore --file FILE`.

## Development

```bash
make dev
```

or without `make`:

```bash
docker compose up --build
```

- Frontend with hot reload: http://localhost:5173 (`CLOUDMESH_WEB_PORT` in `.env`)
- API with reload (air): http://localhost:8080
- Development keys are generated automatically and stored in the `dev_data` volume.

Get the setup code with `make logs` and open http://localhost:5173.

**Try it without Mega accounts**: development enables an in-memory `fake` provider. In **Storage → Add account** choose *Fake*, any email and password `ok`. Connect three to see `2+1` in action. Disable it with `CLOUDMESH_DEV_FAKE_PROVIDER=0`.

| Command | What it does |
|---|---|
| `make dev` / `make down` | Start and stop the environment |
| `make logs` | API logs (the setup code shows up here) |
| `make test` | Go tests, frontend typecheck and lint |
| `make sqlc` | Regenerate SQL code after changing `internal/db/queries` or `migrations` |
| `make user EMAIL=x@y.com` | Create an extra user |
| `make shadcn C="table"` | Add shadcn/ui components |
| `make api-sh` / `make web-sh` | Shell inside each container |
| `make prod` | Build and run the production image locally |
| `make clean` | Remove development containers and volumes, database included |

On Windows without `make`, use the equivalent `docker compose` commands from the [Makefile](Makefile).

### Project layout

```
cmd/cloudmesh/     entry point and subcommands (serve, healthcheck, create-user, backup, restore)
internal/          auth, accounts, vfs, sync, transfer (blocks, scrub, rebuild), erasure, manifest,
                   maintenance, settings, shares, s3, storage (mega, fake), crypto, db, http
web/               Vite + React; `pnpm build` produces web/dist, embedded by Go
docker/            development image and entrypoint
```

Conventions for contributors and coding agents are in [AGENTS.md](AGENTS.md) (Spanish).

## Disclaimer

CloudMesh is an independent open-source project. It is not affiliated with, endorsed by or sponsored by Mega or any other storage provider; all trademarks belong to their owners.

The software is provided "as is", without warranty of any kind (see [LICENSE](LICENSE)). By using it you accept that:

- **You are solely responsible for how you use it.** The authors and contributors are not liable for any misuse, damage, data loss, account suspension or legal consequence arising from its use.
- **You must comply with the terms of service of every provider you connect.** Some providers restrict the number of accounts per person, automated access or how their storage may be used. Check those terms before connecting accounts; CloudMesh does not check them for you.
- **You are responsible for the content you store and share** and for complying with the laws that apply to you, including copyright and data protection.
- **You are responsible for your keys and backups.** Losing `CLOUDMESH_MASTER_KEY` makes the stored data unrecoverable. Redundancy reduces the risk of losing data; it does not remove it. Keep independent backups of anything important.

## License

[MIT](LICENSE) © 2026 Ernesto Suarez Ramirez
