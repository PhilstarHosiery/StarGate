use std::path::Path;

use anyhow::Context;
use serde::Deserialize;

/// All application configuration loaded from a YAML file.
/// Unknown keys are rejected; missing keys fall back to empty values.
#[derive(Debug, Default, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct Config {
    pub server: ServerConfig,
    pub database: DatabaseConfig,
    pub sms: SmsConfig,
}

#[derive(Debug, Default, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct ServerConfig {
    pub grpc_addr: String,    // e.g. ":50051"
    pub webhook_addr: String, // e.g. ":8080"
}

#[derive(Debug, Default, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct DatabaseConfig {
    pub path: String, // e.g. "stargate.db"
}

#[derive(Debug, Default, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct SmsConfig {
    pub gate_url: String,       // SMS Gate local API base URL
    pub username: String,       // Local Mode basic auth username
    pub password: String,       // Local Mode basic auth password
    pub api_key: String,        // Cloud Mode bearer token (overrides basic auth)
    pub webhook_secret: String, // HMAC-SHA256 signing key for inbound webhooks
    pub webhook_url: String,    // Externally-accessible URL SMS Gate will POST to
}

/// Reads and parses the YAML config file at path.
pub fn load(path: &Path) -> anyhow::Result<Config> {
    let text = std::fs::read_to_string(path)
        .with_context(|| format!("config: open {:?}", path.display()))?;
    serde_yaml::from_str(&text).with_context(|| format!("config: decode {:?}", path.display()))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn example_config_parses() {
        let text = include_str!("../config/config.example.yaml");
        let cfg: Config = serde_yaml::from_str(text).unwrap();
        assert_eq!(cfg.server.grpc_addr, ":50051");
        assert_eq!(cfg.database.path, "stargate.db");
        assert_eq!(cfg.sms.api_key, "");
    }

    #[test]
    fn unknown_keys_are_rejected() {
        let err = serde_yaml::from_str::<Config>("server:\n  grpc_port: 1\n");
        assert!(err.is_err());
    }
}
