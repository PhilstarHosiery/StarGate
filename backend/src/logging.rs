use std::fs::{File, OpenOptions};
use std::io::{self, Write};
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex, MutexGuard};

use tracing_subscriber::EnvFilter;
use tracing_subscriber::fmt::MakeWriter;

/// Default filter when RUST_LOG is unset. sqlx is noisy at info.
const DEFAULT_FILTER: &str = "info,sqlx=warn";

/// Installs the global tracing subscriber, writing to stdout or, if a path is
/// given, appending to that file. The returned handle reopens the file on SIGHUP.
pub fn init(path: Option<&Path>) -> io::Result<Option<LogFile>> {
    let filter =
        EnvFilter::try_from_default_env().unwrap_or_else(|_| EnvFilter::new(DEFAULT_FILTER));
    match path {
        Some(path) => {
            let log_file = LogFile::open(path)?;
            tracing_subscriber::fmt()
                .with_env_filter(filter)
                .with_ansi(false)
                .with_writer(log_file.clone())
                .init();
            Ok(Some(log_file))
        }
        None => {
            tracing_subscriber::fmt().with_env_filter(filter).init();
            Ok(None)
        }
    }
}

/// A log file that can be swapped for a freshly opened one after newsyslog rotates it.
#[derive(Clone)]
#[cfg_attr(not(unix), allow(dead_code))] // reopened only on SIGHUP
pub struct LogFile {
    path: PathBuf,
    file: Arc<Mutex<File>>,
}

impl LogFile {
    fn open(path: &Path) -> io::Result<Self> {
        Ok(Self {
            path: path.to_path_buf(),
            file: Arc::new(Mutex::new(open_append(path)?)),
        })
    }

    /// Opens the path again and closes the previous file.
    #[cfg_attr(not(unix), allow(dead_code))]
    pub fn reopen(&self) -> io::Result<()> {
        let new = open_append(&self.path)?;
        *lock(&self.file) = new;
        Ok(())
    }
}

fn open_append(path: &Path) -> io::Result<File> {
    let mut opts = OpenOptions::new();
    opts.create(true).append(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        opts.mode(0o640);
    }
    opts.open(path)
}

fn lock(file: &Mutex<File>) -> MutexGuard<'_, File> {
    file.lock().unwrap_or_else(|poisoned| poisoned.into_inner())
}

pub struct LogWriter(Arc<Mutex<File>>);

impl Write for LogWriter {
    fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
        lock(&self.0).write(buf)
    }

    fn write_all(&mut self, buf: &[u8]) -> io::Result<()> {
        lock(&self.0).write_all(buf)
    }

    fn flush(&mut self) -> io::Result<()> {
        lock(&self.0).flush()
    }
}

impl<'a> MakeWriter<'a> for LogFile {
    type Writer = LogWriter;

    fn make_writer(&'a self) -> Self::Writer {
        LogWriter(self.file.clone())
    }
}
