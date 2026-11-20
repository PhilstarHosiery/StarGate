# StarGate

Centralized, real-time SMS Gateway for Philstar. Remote employees text a central number; messages are routed to the appropriate Supervisors, Managers, or HR via a JavaFX desktop client.

## Repository Layout

```
Stargate/
├── proto/          # Shared gRPC contract (stargate.proto)
├── backend/        # Rust server (webhook receiver + gRPC server)
└── client/         # JavaFX desktop client (Gradle)
```

## Prerequisites

| Tool | Version |
|---|---|
| Java JDK | 25 |
| Rust | stable, via [rustup](https://rustup.rs) (Windows: plus Visual Studio Build Tools, C++ workload) |
| Gradle | via wrapper (`./gradlew`) |

## Building

### Client (JavaFX)

```bash
cd client

# Run locally
./gradlew run

# Build jlink runtime image
./gradlew jlink

# Build native installer (exe)
./gradlew jpackage
```

Output: `client/build/image/` (runtime image), `client/build/jpackage/` (installer).

### Backend (Rust)

```bat
cd backend

run.bat          :: cargo run
build.bat        :: cargo build --release, copies stargate-server.exe
cargo test       :: run tests
```

### Generating gRPC Code

Proto changes are compiled for both languages automatically on build.

**Java**:
```bash
cd client && ./gradlew generateProto
```

**Rust** — `backend/build.rs` regenerates the server code from `proto/stargate.proto` on every `cargo build` (pure-Rust `protox`, no `protoc` needed).

## Configuration

Backend config lives in `backend/config/`. Copy the example and edit:
```bash
cp backend/config/config.example.yaml backend/config/config.yaml
```
`config.yaml` is git-ignored. The server looks for `config/config.yaml` first, then falls back to `config.yaml` in the working directory.

## First-time Setup

Create the initial admin user (interactive prompt):
```bash
cd backend
cargo run -- --create-user
# or on Windows:
run.bat --create-user
```
