# StarGate

Centralized, real-time SMS Gateway for Philstar. Remote employees text a central number; messages are routed to the appropriate Supervisors, Managers, or HR via a JavaFX desktop client. Features RBAC, Dual-SIM routing (Globe/Smart), and real-time UI updates via gRPC streaming.

## Tech Stack

| Layer | Technology |
|---|---|
| Hardware (current) | Android phone running SMS Gate app (Private Mode) |
| Hardware (future) | Teltonika RUT241 industrial 4G LTE router |
| Backend | Go — FreeBSD, cross-compilable to Windows `.exe` |
| Database | SQLite (or PostgreSQL) |
| Frontend | Java 25 + JavaFX desktop client |
| Transport | gRPC — server-side streaming for real-time updates |

## Database Schema

Flat ACL model (no rigid role hierarchy).

- **Users** — JavaFX operators. `(user_id, name, has_global_access)` — `has_global_access = true` for HR.
- **Sections** — Company departments. `(section_id, section_name)` e.g. "Welding".
- **User_Sections** — Maps users to sections. Supervisors: 1 section; Managers: many; HR: ignored.
- **Employees** — Workers texting the system. `(phone_number PK, name, section_id nullable)`.
- **Sessions** — Conversation threads. `(session_id, employee_phone, assigned_sim, status OPEN|CLOSED)`.
- **Messages** — `(message_id, session_id, direction INBOUND|OUTBOUND, text, sent_by_user_id, timestamp)`.

## Core Workflows

### Inbound Routing
1. SMS Gate / RUT241 POSTs a webhook to the Go backend.
2. Go looks up the sender in `Employees`.
   - **Found** → route to that employee's section.
   - **Not found** → create an "Unknown" employee with `section_id = NULL`; push only to HR (`has_global_access = true`).
3. Go pushes the message via gRPC stream to all connected clients with permission for that section.
4. HR assigns an Unknown number a name and section → Go updates the DB and re-routes the session.

### Outbound (Sticky SIM)
- Inbound messages record which SIM received them (`assigned_sim`: 1 = Globe, 2 = Smart).
- Replies are sent via `SendReply` gRPC call → Go POSTs to the SMS Gate / RUT241 API with the same `simNumber`, avoiding cross-network fees.

### Real-Time Communication
- JavaFX connects on login via a persistent gRPC `StreamObserver`.
- Go pushes `ChatEvent` payloads immediately on webhook receipt.
- JavaFX applies updates with `Platform.runLater()`.

## gRPC Contract (`proto/stargate.proto`)

```proto
syntax = "proto3";
package stargate;

service StarGateCore {
  rpc SubscribeToInbox(UserContext)    returns (stream ChatEvent);
  rpc SendReply(ReplyRequest)          returns (ActionResponse);
  rpc AssignEmployee(AssignRequest)    returns (ActionResponse);
}

message UserContext   { string user_id = 1; }
message ActionResponse { bool success = 1; string error_message = 2; }

message ChatEvent {
  string session_id    = 1;
  string phone_number  = 2;
  string employee_name = 3;
  string message_text  = 4;
  string section_id    = 5;
  int32  active_sim    = 6;  // 1 = Globe, 2 = Smart
  string sender_type   = 7;  // "Employee" | "HR" | etc.
}

message AssignRequest {
  string phone_number       = 1;
  string employee_name      = 2;
  string section_id         = 3;
  string assigned_by_user_id = 4;
}

message ReplyRequest {
  string session_id   = 1;
  string message_text = 2;
  string user_id      = 3;
}
```
