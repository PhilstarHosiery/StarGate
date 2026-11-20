//! HTTP server for inbound SMS webhooks from SMS Gate, plus /healthz.

use std::sync::Arc;

use axum::Router;
use axum::body::Bytes;
use axum::extract::State;
use axum::http::{HeaderMap, StatusCode};
use axum::routing::{any, post};
use chrono::{DateTime, Utc};
use hmac::{Hmac, Mac};
use serde::Deserialize;
use sha2::Sha256;
use tracing::{error, info, warn};

use crate::db::{Db, format_timestamp};
use crate::grpc::StreamManager;
use crate::pb;

/// Webhooks whose X-Timestamp is further than this from now are rejected.
const MAX_CLOCK_SKEW_SECS: i64 = 5 * 60;

#[derive(Clone)]
struct WebhookState {
    db: Db,
    streams: Arc<StreamManager>,
    /// HMAC-SHA256 signing key from SMS Gate → Settings → Webhooks.
    /// Empty skips signature verification (not recommended in production).
    secret: Arc<str>,
}

/// Top-level JSON body sent by SMS Gate.
#[derive(Deserialize, Default)]
#[serde(default)]
struct SmsGateEvent {
    event: Option<String>,
    payload: SmsGatePayload,
}

#[derive(Deserialize, Default)]
#[serde(default, rename_all = "camelCase")]
struct SmsGatePayload {
    message_id: Option<String>,
    sender: Option<String>,
    message: Option<String>,
    sim_number: Option<i64>,
    received_at: Option<String>,
}

pub fn router(db: Db, streams: Arc<StreamManager>, webhook_secret: String) -> Router {
    let state = WebhookState {
        db,
        streams,
        secret: webhook_secret.into(),
    };
    Router::new()
        .route("/webhook", post(handle_webhook))
        .route("/healthz", any(|| async { StatusCode::OK }))
        .with_state(state)
}

type Reply = (StatusCode, &'static str);

const OK: Reply = (StatusCode::OK, "");
const BAD_REQUEST: Reply = (StatusCode::BAD_REQUEST, "bad request");
const INTERNAL: Reply = (StatusCode::INTERNAL_SERVER_ERROR, "internal error");

async fn handle_webhook(State(st): State<WebhookState>, headers: HeaderMap, body: Bytes) -> Reply {
    if !st.secret.is_empty()
        && !verify_signature(&st.secret, &body, &headers, Utc::now().timestamp())
    {
        warn!("webhook: signature verification failed");
        return (StatusCode::UNAUTHORIZED, "unauthorized");
    }

    let event: SmsGateEvent = match serde_json::from_slice(&body) {
        Ok(event) => event,
        Err(e) => {
            warn!(err = %e, "webhook: bad JSON body");
            return BAD_REQUEST;
        }
    };

    // Only handle inbound SMS events.
    if event.event.as_deref() != Some("sms:received") {
        return OK;
    }

    let payload = event.payload;
    let phone = payload.sender.unwrap_or_default();
    let message = payload.message.unwrap_or_default();
    let sim = payload.sim_number.unwrap_or_default();
    if phone.is_empty() || message.is_empty() {
        return (StatusCode::BAD_REQUEST, "missing sender or message");
    }

    info!(from = %phone, sim, "webhook: inbound SMS");

    // Look up or create the contact; new numbers become unknown contacts
    // pinned to whichever SIM received them.
    let contact = match st.db.get_contact_by_phone(&phone).await {
        Ok(Some(contact)) => contact,
        Ok(None) => match st.db.create_contact(&phone, sim).await {
            Ok(contact) => {
                info!(phone = %phone, "webhook: created unknown contact");
                contact
            }
            Err(e) => {
                error!(err = %e, "webhook: CreateContact failed");
                return INTERNAL;
            }
        },
        Err(e) => {
            error!(err = %e, "webhook: GetContactByPhone failed");
            return INTERNAL;
        }
    };

    // Look up or create an open session.
    let session = match st.db.get_open_session_by_phone(&phone).await {
        Ok(Some(session)) => session,
        Ok(None) => match st.db.create_session(&phone).await {
            Ok(session) => {
                info!(session_id = %session.session_id, "webhook: created new session");
                session
            }
            Err(e) => {
                error!(err = %e, "webhook: CreateSession failed");
                return INTERNAL;
            }
        },
        Err(e) => {
            error!(err = %e, "webhook: GetOpenSessionByPhone failed");
            return INTERNAL;
        }
    };

    // SMS Gate's receive time is authoritative; fall back to now if missing/malformed.
    let received_at = payload
        .received_at
        .as_deref()
        .and_then(|s| DateTime::parse_from_rfc3339(s).ok())
        .map(|t| t.with_timezone(&Utc))
        .unwrap_or_else(Utc::now);

    let gateway_id = payload.message_id.filter(|id| !id.is_empty());
    let stored = st
        .db
        .create_message(
            &session.session_id,
            "INBOUND",
            &message,
            None,
            gateway_id.as_deref(),
            received_at,
        )
        .await;
    let msg = match stored {
        Ok(Some(msg)) => msg,
        Ok(None) => {
            info!(gateway_id = ?gateway_id, "webhook: duplicate message ignored");
            return OK;
        }
        Err(e) => {
            error!(err = %e, "webhook: CreateMessage failed");
            return INTERNAL;
        }
    };

    match st
        .db
        .get_users_with_access_to_group(contact.group_id.as_deref())
        .await
    {
        // Don't fail the request — the message is stored; clients will see it on reload.
        Err(e) => error!(err = %e, "webhook: GetUsersWithAccessToGroup failed"),
        Ok(targets) => {
            let event = pb::MessageEvent {
                session_id: session.session_id,
                message_text: message,
                sender_type: "Contact".to_string(),
                timestamp: format_timestamp(msg.timestamp),
                sender_username: String::new(),
            };
            st.streams.broadcast(&event, &targets);
        }
    }

    OK
}

/// Checks X-Signature against HMAC-SHA256(secret, body + X-Timestamp) and
/// rejects requests whose X-Timestamp is more than 5 minutes from `now`.
fn verify_signature(secret: &str, body: &[u8], headers: &HeaderMap, now: i64) -> bool {
    let header = |name: &str| {
        headers
            .get(name)
            .and_then(|v| v.to_str().ok())
            .unwrap_or("")
    };
    let ts_str = header("X-Timestamp");
    let signature = header("X-Signature");
    if ts_str.is_empty() || signature.is_empty() {
        return false;
    }

    let Ok(ts) = ts_str.parse::<i64>() else {
        return false;
    };
    let age = now.abs_diff(ts);
    if age > MAX_CLOCK_SKEW_SECS as u64 {
        warn!(age_secs = age, "webhook: stale timestamp rejected");
        return false;
    }

    let Ok(signature) = hex::decode(signature) else {
        return false;
    };
    let mut mac =
        Hmac::<Sha256>::new_from_slice(secret.as_bytes()).expect("HMAC accepts any key length");
    mac.update(body);
    mac.update(ts_str.as_bytes());
    mac.verify_slice(&signature).is_ok()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn signed_headers(secret: &str, body: &[u8], ts: i64) -> HeaderMap {
        let mut mac = Hmac::<Sha256>::new_from_slice(secret.as_bytes()).unwrap();
        mac.update(body);
        mac.update(ts.to_string().as_bytes());
        let mut headers = HeaderMap::new();
        headers.insert("X-Timestamp", ts.to_string().parse().unwrap());
        headers.insert(
            "X-Signature",
            hex::encode(mac.finalize().into_bytes()).parse().unwrap(),
        );
        headers
    }

    #[test]
    fn valid_signature_is_accepted() {
        let headers = signed_headers("k", b"{}", 1_000_000);
        assert!(verify_signature("k", b"{}", &headers, 1_000_000 + 60));
    }

    #[test]
    fn tampered_body_wrong_key_or_stale_timestamp_is_rejected() {
        let headers = signed_headers("k", b"{}", 1_000_000);
        assert!(!verify_signature("k", b"{ }", &headers, 1_000_000));
        assert!(!verify_signature("other", b"{}", &headers, 1_000_000));
        assert!(!verify_signature("k", b"{}", &headers, 1_000_000 + 301));
        assert!(!verify_signature("k", b"{}", &HeaderMap::new(), 1_000_000));
    }

    #[test]
    fn sms_gate_payload_parses() {
        let event: SmsGateEvent = serde_json::from_str(
            r#"{"event":"sms:received","payload":{"messageId":"abc","sender":"+639170000000",
                "message":"hello","simNumber":2,"receivedAt":"2026-01-02T15:04:05.123+08:00"},
                "deviceId":"x"}"#,
        )
        .unwrap();
        assert_eq!(event.event.as_deref(), Some("sms:received"));
        assert_eq!(event.payload.sim_number, Some(2));
        assert_eq!(event.payload.sender.as_deref(), Some("+639170000000"));
    }
}
