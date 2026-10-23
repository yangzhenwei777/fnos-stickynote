# Sticky note

A lightweight, polished multi-user note-taking app for fnOS NAS (home/team use). Rich-text editing, full-text search, note encryption, TOTP two-factor auth, and per-user data isolation. It can be deployed as an fnOS FPK app, a standalone binary, or a Windows desktop client.

Current version: **2.2.0**

## Features

- **Rich-text editor**: Tiptap WYSIWYG with headings, bold, italic, underline, strikethrough, quotes, code blocks, lists, colors, images, and tables
- **Full-text search**: SQLite FTS5 with LIKE fallback for short queries
- **Note encryption**: AES-256-GCM + PBKDF2 (600,000 iterations) + bcrypt; encrypted notes never persist plaintext
- **Two-factor auth**: TOTP + recovery codes
- **Concurrent-edit safety**: CAS optimistic locking with 409 conflicts
- **Attachment lifecycle**: staged → attached → orphan → delete_pending state machine with magic-number validation
- **Trash**: soft delete with restore; permanent delete cascades attachment cleanup
- **Markdown import/export**: single note + batch ZIP
- **Multi-user**: full data isolation; the first registered user becomes admin
- **PWA / dark mode / responsive**
- **Windows desktop client**: offline-first Wails client with local SQLite and sync

## Tech Stack

- **Frontend**: Vue 3 + Element Plus + Tiptap + Vite 8 + Pinia
- **Backend**: Go + Gin + SQLite (modernc.org/sqlite, pure Go, no CGO, single binary)
- **Auth**: dual JWT (access 2h / refresh 7–30 days) + bcrypt + token_version global revocation
- **Security**: bluemonday XSS sanitization, CSP, three-layer anti-brute-force, external images stripped
- **Desktop**: Wails v2 + Vue 3 + local SQLite

## Docker deployment

The image is published on Docker Hub: `davyang/sticky-note`, available for `linux/amd64` and `linux/arm64`.

```bash
docker run -d --name sticky-note \
  -p 13389:13389 \
  -v sticky-note-data:/app/data \
  -e TZ=Asia/Shanghai \
  davyang/sticky-note:2.2.0
```

Or use the `docker-compose.yaml` in this repo (pinned to 2.2.0 by default). Notes:

- **Pin an explicit version tag in production** (e.g. `2.2.0`); `latest` is for quick trial only
- **Persistence**: all data lives in `/app/data`; back up by stopping the container and copying that directory
- **Container form**: the fnOS unix-socket gateway mode is disabled inside containers — pure TCP direct access (gateway mode is exclusive to the fpk deployment)
- **Upgrade**: bump the version, then `docker compose pull && docker compose up -d` (volume compatible, no migration)
- Visit `http://<host>:13389` to register; the first user becomes admin
## Quick Start

### Run the binary

```bash
./sticky-note
# http://localhost:13389
# The first registered user becomes admin.
```

### fnOS NAS

1. Download `StickyNote-<date>-<version>.fpk` (x86/amd64 currently)
2. App Center → Manual install → upload the FPK
3. Set the port in the wizard (default `13389`); the JWT secret is generated and persisted automatically

### Build from source

```bash
# Requires Go 1.26.10+ and Node.js 22+
make build          # current platform
make cross-compile  # Linux amd64/arm64, macOS amd64/arm64
make build-fpk      # fnOS FPK package
```

Build output goes to `release/<version>/`.

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `13389` | Service port |
| `DATA_DIR` | `./data` | Data directory; SQLite database and master key derive from it |
| `UPLOAD_DIR` | `$DATA_DIR/upload` | Attachment storage directory |
| `JWT_SECRET` | random | JWT signing secret; generated and persisted to `$DATA_DIR/.jwt_secret` when unset |
| `COOKIE_SECURE` | auto | Force the Cookie Secure flag; defaults to HTTPS detection |

> For production, enable HTTPS through the fnOS reverse proxy and set `COOKIE_SECURE=1`.

## Desktop Client

The Windows desktop client lives in `desktop/` and is built with Wails v2. See [desktop/README.md](desktop/README.md).

## Project Layout

```text
├── server/              # Go backend
├── web/                 # Vue 3 frontend
├── desktop/             # Wails desktop client
├── fnpack/              # fnOS FPK template
├── scripts/             # build, test and quality gates
├── docs/                # public documentation
├── VERSION              # single version source
└── Makefile             # build entry point
```

## Testing and Quality Gates

- Go unit tests (including race detection)
- Frontend Vitest + ESLint
- Smoke tests and Playwright E2E
- `govulncheck` / `npm audit` baseline diff
- `scripts/check_secrets.py` credential scan
- VERSION and `fnpack/manifest` consistency check

## Security

Please report security issues privately through GitHub Security Advisories. See [SECURITY.md](SECURITY.md).

## License

MIT. See [LICENSE](LICENSE).
