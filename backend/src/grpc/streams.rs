use std::collections::HashMap;
use std::pin::Pin;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex, MutexGuard};
use std::task::{Context, Poll};

use tokio::sync::mpsc;
use tokio_stream::Stream;
use tonic::Status;
use tracing::{info, warn};

use crate::pb::MessageEvent;

/// Events buffered per client before further events to it are dropped.
const STREAM_BUFFER: usize = 256;

type EventTx = mpsc::Sender<Result<MessageEvent, Status>>;

/// Tracks active SubscribeToInbox streams keyed by user ID.
#[derive(Default)]
pub struct StreamManager {
    streams: Mutex<HashMap<String, (u64, EventTx)>>,
    next_id: AtomicU64,
}

impl StreamManager {
    pub fn new() -> Self {
        Self::default()
    }

    fn lock(&self) -> MutexGuard<'_, HashMap<String, (u64, EventTx)>> {
        self.streams
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner())
    }

    /// Registers a stream for the user, replacing (and ending) any previous
    /// stream for that user. The stream unregisters itself when dropped.
    pub fn register(self: &Arc<Self>, user_id: &str) -> InboxStream {
        let (tx, rx) = mpsc::channel(STREAM_BUFFER);
        let conn_id = self.next_id.fetch_add(1, Ordering::Relaxed);
        self.lock().insert(user_id.to_string(), (conn_id, tx));
        info!(user_id = %user_id, "stream registered");
        InboxStream {
            rx,
            _registration: Registration {
                manager: self.clone(),
                user_id: user_id.to_string(),
                conn_id,
            },
        }
    }

    /// Removes the user's stream, unless it has since been replaced by a newer one.
    fn unregister(&self, user_id: &str, conn_id: u64) {
        let mut streams = self.lock();
        if streams.get(user_id).is_some_and(|(id, _)| *id == conn_id) {
            streams.remove(user_id);
            info!(user_id = %user_id, "stream unregistered");
        }
    }

    /// Sends event to every listed user that has an active stream. A failed
    /// send to one user is logged and does not stop delivery to the others.
    pub fn broadcast(&self, event: &MessageEvent, user_ids: &[String]) {
        let streams = self.lock();
        for user_id in user_ids {
            let Some((_, tx)) = streams.get(user_id) else {
                continue;
            };
            if let Err(e) = tx.try_send(Ok(event.clone())) {
                warn!(user_id = %user_id, err = %e, "broadcast send failed");
            }
        }
    }

    /// Ends every stream. Used on shutdown so the gRPC server can drain.
    pub fn close_all(&self) {
        self.lock().clear();
    }
}

/// The response stream for one SubscribeToInbox call.
pub struct InboxStream {
    rx: mpsc::Receiver<Result<MessageEvent, Status>>,
    _registration: Registration,
}

impl Stream for InboxStream {
    type Item = Result<MessageEvent, Status>;

    fn poll_next(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Option<Self::Item>> {
        self.get_mut().rx.poll_recv(cx)
    }
}

/// Unregisters the stream when tonic drops it (client disconnected).
struct Registration {
    manager: Arc<StreamManager>,
    user_id: String,
    conn_id: u64,
}

impl Drop for Registration {
    fn drop(&mut self) {
        info!(user_id = %self.user_id, "SubscribeToInbox: client disconnected");
        self.manager.unregister(&self.user_id, self.conn_id);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use tokio_stream::StreamExt;

    fn event(text: &str) -> MessageEvent {
        MessageEvent {
            message_text: text.to_string(),
            ..Default::default()
        }
    }

    #[tokio::test]
    async fn broadcast_reaches_only_listed_users() {
        let mgr = Arc::new(StreamManager::new());
        let mut a = mgr.register("a");
        let mut b = mgr.register("b");

        mgr.broadcast(&event("hello"), &["a".to_string(), "nobody".to_string()]);
        mgr.close_all();

        assert_eq!(a.next().await.unwrap().unwrap().message_text, "hello");
        assert!(a.next().await.is_none());
        assert!(b.next().await.is_none());
    }

    #[tokio::test]
    async fn stale_stream_drop_keeps_newer_registration() {
        let mgr = Arc::new(StreamManager::new());
        let old = mgr.register("a");
        let mut new = mgr.register("a");
        drop(old);

        mgr.broadcast(&event("still here"), &["a".to_string()]);
        mgr.close_all();
        assert_eq!(
            new.next().await.unwrap().unwrap().message_text,
            "still here"
        );
    }
}
