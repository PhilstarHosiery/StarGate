# StarGate

Centralized, real-time SMS Gateway for Philstar. Remote contacts text a central number; messages are routed to the appropriate Supervisors, Managers, or HR via a JavaFX desktop client. Features RBAC, Dual-SIM routing (Globe/Smart), and real-time UI updates via gRPC streaming.

## Tech Stack

| Layer | Technology |
|---|---|
| Hardware (current) | Android phone running SMS Gate app (Private Mode) |
| Hardware (future) | Teltonika RUT241 industrial 4G LTE router |
| Backend | Rust (tokio, tonic, axum, sqlx) — FreeBSD and Windows `.exe` |
| Database | SQLite (or PostgreSQL) |
| Frontend | Java 25 + JavaFX desktop client |
| Transport | gRPC — server-side streaming for real-time updates |

## Core Concepts

**User** — A person operating the system via the JavaFX client. Has a username, password, and an internal user ID. Access is granted per Group.

**Contact** — Anyone who texts the system (employee, supplier, marketing, unknown). Identified by phone number. A name can be assigned later. Belongs to at most one Group. Has a preferred SIM (Globe or Smart) used for all replies.

**Group** — A category that Contacts belong to (e.g. "Welding", "Logistics"). Users are granted access to one or more Groups; they only see messages from Contacts in those Groups.

**Session** — A collection of Messages belonging to a Contact. Represents the ongoing conversation thread with that Contact.

**Message** — A single text message sent or received between a User and a Contact. Belongs to a Session.

## Database Schema

- **Users** — `(user_id, username, password_hash, has_global_access)` — `has_global_access = true` grants access to all groups (HR use case).
- **Groups** — `(group_id, group_name)`.
- **User_Groups** — Access control mapping. `(user_id, group_id)`.
- **Contacts** — `(contact_phone PK, name nullable, group_id nullable, assigned_sim NOT NULL DEFAULT 0)`.
- **Sessions** — Conversation threads. `(session_id, contact_phone, status OPEN|CLOSED)`.
- **Messages** — `(message_id, session_id, direction INBOUND|OUTBOUND, text, sent_by_user_id nullable, timestamp, gateway_message_id nullable unique)` — `gateway_message_id` is used for webhook deduplication.

## Core Workflows

### Inbound Routing
1. SMS Gate / RUT241 POSTs a webhook to the Rust backend.
2. The backend looks up the sender in `Contacts`.
   - **Found** → route to that contact's group.
   - **Not found** → create an "Unknown" contact with `group_id = NULL`; push only to HR (`has_global_access = true`).
3. The backend pushes the message via gRPC stream to all connected clients with permission for that group.
4. HR calls `RenameContact` and `AssignContact` → the backend updates the DB and pushes a `MessageEvent` to all users with permission to the newly assigned group.

### Outbound (Sticky SIM)
- Each Contact has an `assigned_sim` (1 = Globe, 2 = Smart) set at creation time from whichever SIM received the first inbound message.
- Replies are sent via `SendReply` gRPC call → the backend POSTs to the SMS Gate / RUT241 API with the Contact's `assigned_sim`, avoiding cross-network fees.

### Real-Time Communication
- JavaFX connects on login via a persistent gRPC `StreamObserver`.
- The backend pushes `MessageEvent` payloads immediately on webhook receipt.
- JavaFX applies updates with `Platform.runLater()`.

## gRPC Contract

Defined in `proto/stargate.proto`. The service is `StarGateCoreServer`.

| RPC | Type | Description |
|-----|------|-------------|
| `Login` | Unary | Validate credentials, return user info |
| `ChangePassword` | Unary | Verify current password, set new hash |
| `SubscribeToInbox` | Server-streaming | Persistent stream for real-time `MessageEvent` pushes |
| `GetSessions` | Unary | All sessions the caller has access to |
| `GetSession` | Unary | Single session by ID |
| `GetMessages` | Unary | All messages in a session |
| `SendReply` | Unary | Store outbound message, send via SMS gateway, broadcast to peers |
| `RenameContact` | Unary | Set display name on a contact |
| `AssignContact` | Unary | Move contact to a group; broadcasts re-routing event |
| `RetireContact` | Unary | Archive old contact (rename to `phone-old`), create fresh unknown contact |
| `CreateSession` | Unary | Start an outbound conversation to a new phone number |
| `ListUsers` | Unary | Admin: all users with their group mappings |
| `CreateUser` | Unary | Admin: add a new user |
| `DeleteUser` | Unary | Admin: remove a user |
| `ListGroups` | Unary | List accessible groups (all for HR, filtered otherwise) |
| `CreateGroup` | Unary | Admin: add a new group |
| `RenameGroup` | Unary | Admin: rename a group |
| `DeleteGroup` | Unary | Admin: remove a group (must be empty) |
| `SetUserPermissions` | Unary | Admin: set global flag and group memberships atomically |

All admin RPCs require `has_global_access = true`; the server enforces this via `requireGlobalAccess()`.
