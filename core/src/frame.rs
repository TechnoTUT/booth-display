//! Canvas frames shared read-only between the encoder, the preview and the
//! mixer, and recycled through a pool instead of being reallocated (a 5760x540
//! BGRX frame is 12 MB).

use std::ops::Deref;
use std::sync::{Arc, Mutex};

/// Frames kept for reuse; more than this are freed.
const POOL_LIMIT: usize = 8;

#[derive(Clone)]
pub struct FramePool {
    size: usize,
    free: Arc<Mutex<Vec<Vec<u8>>>>,
}

impl FramePool {
    pub fn new(size: usize) -> Self {
        Self { size, free: Arc::default() }
    }

    /// A buffer of the pool's size with unspecified contents.
    pub fn get(&self) -> PooledBuf {
        let buf = self.free.lock().unwrap().pop().unwrap_or_else(|| vec![0; self.size]);
        PooledBuf { buf, pool: self.free.clone() }
    }
}

/// A pooled buffer; goes back to its pool when dropped.
pub struct PooledBuf {
    buf: Vec<u8>,
    pool: Arc<Mutex<Vec<Vec<u8>>>>,
}

impl PooledBuf {
    pub fn as_mut_slice(&mut self) -> &mut [u8] {
        &mut self.buf
    }
}

impl Deref for PooledBuf {
    type Target = [u8];
    fn deref(&self) -> &[u8] {
        &self.buf
    }
}

impl Drop for PooledBuf {
    fn drop(&mut self) {
        let mut free = self.pool.lock().unwrap();
        if free.len() < POOL_LIMIT {
            free.push(std::mem::take(&mut self.buf));
        }
    }
}

/// A finished canvas frame, shared by reference count.
pub type Frame = Arc<PooledBuf>;
