//! bcrypt helpers. Hashing is CPU-bound, so it runs on the blocking pool.

/// Matches Go's `bcrypt.DefaultCost`, which produced every existing hash.
const BCRYPT_COST: u32 = 10;

pub async fn hash(password: String) -> anyhow::Result<String> {
    Ok(tokio::task::spawn_blocking(move || bcrypt::hash(password, BCRYPT_COST)).await??)
}

/// Returns false for a wrong password and for a malformed stored hash.
pub async fn verify(password: String, hash: String) -> bool {
    matches!(
        tokio::task::spawn_blocking(move || bcrypt::verify(password, &hash)).await,
        Ok(Ok(true))
    )
}
