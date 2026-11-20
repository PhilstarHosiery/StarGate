//! Outbound SMS via the SMS Gate (android-sms-gateway) REST API.

use std::time::Duration;

use anyhow::{Context, bail};
use reqwest::{RequestBuilder, Response};
use serde::Serialize;
use serde_json::json;

use crate::config::SmsConfig;

const REQUEST_TIMEOUT: Duration = Duration::from_secs(30);

pub struct OutboundClient {
    http: reqwest::Client,
    gate_url: String,
    auth: Auth,
}

/// Cloud Mode uses a bearer token; Local Mode uses basic auth.
enum Auth {
    Bearer(String),
    Basic { username: String, password: String },
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct SendRequest<'a> {
    phone_numbers: [&'a str; 1],
    text_message: TextMessage<'a>,
    #[serde(skip_serializing_if = "Option::is_none")]
    sim_number: Option<u8>,
}

#[derive(Serialize)]
struct TextMessage<'a> {
    text: &'a str,
}

impl OutboundClient {
    /// If `api_key` is set, Cloud Mode (bearer token) is used; otherwise Local
    /// Mode (basic auth with username/password).
    pub fn new(cfg: &SmsConfig) -> anyhow::Result<Self> {
        let auth = if cfg.api_key.is_empty() {
            Auth::Basic {
                username: cfg.username.clone(),
                password: cfg.password.clone(),
            }
        } else {
            Auth::Bearer(cfg.api_key.clone())
        };
        Ok(Self {
            http: reqwest::Client::builder()
                .timeout(REQUEST_TIMEOUT)
                .build()
                .context("sms: build http client")?,
            gate_url: cfg.gate_url.trim_end_matches('/').to_string(),
            auth,
        })
    }

    fn post(&self, path: &str) -> RequestBuilder {
        let req = self.http.post(format!("{}{path}", self.gate_url));
        match &self.auth {
            Auth::Bearer(token) => req.bearer_auth(token),
            Auth::Basic { username, password } => req.basic_auth(username, Some(password)),
        }
    }

    /// Registers this server's webhook URL with SMS Gate so it POSTs inbound SMS to us.
    pub async fn register_webhook(&self, webhook_url: &str) -> anyhow::Result<()> {
        let resp = self
            .post("/webhooks")
            .json(&json!({
                "id": "stargate-inbound",
                "event": "sms:received",
                "url": webhook_url,
            }))
            .send()
            .await
            .context("sms: register webhook")?;
        if resp.status().as_u16() >= 300 {
            bail!("sms: register webhook: unexpected status {}", resp.status());
        }
        Ok(())
    }

    /// Sends an SMS to one recipient. `sim` is 1 (Globe) or 2 (Smart); 0 (unset)
    /// lets the gateway pick its default SIM.
    pub async fn send(&self, to: &str, sim: i64, text: &str) -> anyhow::Result<()> {
        let body = SendRequest {
            phone_numbers: [to],
            text_message: TextMessage { text },
            sim_number: u8::try_from(sim).ok().filter(|&n| n > 0),
        };
        let resp = self
            .post("/messages")
            .json(&body)
            .send()
            .await
            .context("sms: send")?;
        check_status(resp).await.context("sms: send")
    }
}

async fn check_status(resp: Response) -> anyhow::Result<()> {
    let status = resp.status();
    if status.is_client_error() || status.is_server_error() {
        let body = resp.text().await.unwrap_or_default();
        bail!("unexpected status {status}: {}", body.trim());
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn send_request_matches_sms_gate_schema() {
        let body = SendRequest {
            phone_numbers: ["+639170000000"],
            text_message: TextMessage { text: "hi" },
            sim_number: Some(2),
        };
        assert_eq!(
            serde_json::to_value(&body).unwrap(),
            json!({"phoneNumbers": ["+639170000000"], "textMessage": {"text": "hi"}, "simNumber": 2})
        );

        let body = SendRequest {
            sim_number: None,
            ..body
        };
        assert!(
            serde_json::to_value(&body)
                .unwrap()
                .get("simNumber")
                .is_none()
        );
    }
}
