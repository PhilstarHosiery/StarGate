# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

See the root `../CLAUDE.md` for project overview, domain concepts, and core workflows.

## Commands

### Backend (Rust)

```bash
# Run (from backend/)
cargo run
# or on Windows:
run.bat

# Build release binary (target/release/stargate-server)
cargo build --release
# or on Windows (also copies stargate-server.exe into backend/):
build.bat

# Run tests
cargo test

# Run one module's tests
cargo test db::

# Lint
cargo fmt --check
cargo clippy --all-targets -- -D warnings
```

gRPC code is generated at build time by `build.rs` from `../proto/stargate.proto` (via `tonic-prost-build` + the pure-Rust `protox` compiler — no `protoc` needed). Proto changes are picked up automatically on the next `cargo build`.

On Windows the MSVC toolchain needs Visual Studio Build Tools with the "Desktop development with C++" workload (SQLite and `ring` are compiled from C).

### Create the first admin user (interactive CLI prompt)

```bash
cargo run -- --create-user
# With explicit config path:
cargo run -- --config /usr/local/etc/stargate/config.yaml --create-user
```

Go-style single-dash flags (`-config`, `-log`, `-pid-file`, `-create-user`) are also accepted, so existing rc.d scripts keep working.

## Architecture

The backend is a single Rust (tokio) binary that runs two servers concurrently:
- **gRPC server** (`:50051`, tonic) — all client-facing RPCs
- **HTTP server** (`:8080`, axum) — SMS Gate webhook endpoint

### Key files and responsibilities

| File | Responsibility |
|------|---------------|
| `src/main.rs` | Entry point: CLI flags, wires config, DB, SMS client, gRPC + HTTP servers, signals/shutdown |
| `src/config.rs` | YAML config loader (`config/config.yaml` → `config.yaml` fallback); unknown keys are rejected |
| `src/logging.rs` | tracing setup; log file that reopens on SIGHUP |
| `src/models.rs` | Shared data structs (User, Contact, Group, Session, Message) |
| `src/db.rs` | All database queries (sqlx/SQLite); runs embedded `migrations/*.sql` on startup |
| `src/password.rs` | bcrypt hash/verify on the blocking pool (cost 10) |
| `src/grpc/server.rs` | Implements every RPC (`StarGateCore` trait) |
| `src/grpc/streams.rs` | `StreamManager` — thread-safe map of active `SubscribeToInbox` streams |
| `src/sms/webhook.rs` | HTTP router: inbound SMS webhook + `/healthz` |
| `src/sms/outbound.rs` | Sends outbound SMS / registers the webhook via the SMS Gate REST API |
| `build.rs` | Generates the `pb` module from the proto — do not hand-edit generated code |

### Real-time broadcast flow

`webhook.rs` → `db.rs` (store message) → `streams.rs` `broadcast()` → gRPC `SubscribeToInbox` streams on connected clients.

`server.rs` `send_reply()` follows the same path in reverse: store outbound message → `outbound.rs` `send()` → `broadcast()` to all *other* connected users so their UIs update.

Each user has at most one stream; a new subscription for the same user replaces (and ends) the previous one. Events are queued per stream (bounded); if a client stops reading, further events to it are dropped with a warning.

### Access control

`user_has_access(user_id, group_id)` in `db.rs` is the single gate. A user passes if they appear in `user_groups` for that group **or** `has_global_access = true`. Admin RPCs go through `require_global_access()`.

### Deduplication

`create_message()` accepts an optional `gateway_message_id`. The column has a unique index; duplicate webhook deliveries are silently ignored with an `INSERT OR IGNORE`.

### Database notes

- Foreign keys are enforced on every pooled connection. `retire_contact` uses `PRAGMA defer_foreign_keys` because it re-points sessions before renaming the contact.
- Timestamps are stored and sent as RFC 3339 UTC with whole seconds (`2026-01-02T15:04:05Z`); legacy `YYYY-MM-DD HH:MM:SS` rows are read as UTC.
- Migrations are tracked in `schema_migrations` (same table the old Go backend used), so existing `stargate.db` files work unchanged. Add new migrations to `migrations/` **and** the `MIGRATIONS` list in `db.rs`.

### Deployment (FreeBSD)

The binary accepts a `--config <path>` flag; without it, it falls back to `config/config.yaml` then `config.yaml` (useful for local dev).

```bash
# Build natively on the FreeBSD host (pkg install rust)
cargo build --release

# Standard install paths
/usr/local/bin/stargate-server
/usr/local/etc/stargate/config.yaml
/var/db/stargate/stargate.db        # set database.path in config.yaml

# rc.d service (see deploy/freebsd/rc.d/stargate)
cp deploy/freebsd/rc.d/stargate /usr/local/etc/rc.d/stargate
chmod +x /usr/local/etc/rc.d/stargate
# Add to /etc/rc.conf: stargate_enable="YES"
service stargate start
```

The server handles SIGTERM/SIGINT gracefully: it ends all inbox streams, lets in-flight gRPC and HTTP requests finish (10 s timeout each), then exits. SIGHUP reopens the `--log` file (used by newsyslog).

`GET /healthz` on the webhook port returns 200 OK — use it for uptime monitoring.

Log verbosity follows `RUST_LOG` (default `info,sqlx=warn`).

### Configuration

Copy `config/config.example.yaml` → `config/config.yaml`. Key fields:

```yaml
server:
  grpc_addr: ":50051"
  webhook_addr: ":8080"
database:
  path: "stargate.db"
sms:
  gate_url: "http://192.168.1.1"    # SMS Gate app / RUT241 API base URL
  username: ""                       # Local Mode basic auth (SMS Gate → Settings → Local Server)
  password: ""
  api_key: ""                        # Cloud Mode bearer token — leave empty for Local Mode
  webhook_secret: ""                 # HMAC-SHA256 signing key from SMS Gate settings
  webhook_url: "https://your-server/webhook"  # Externally-accessible URL SMS Gate will POST to
```

An address like `":50051"` binds all IPv4 interfaces.
