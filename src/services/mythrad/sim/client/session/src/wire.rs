//! Wire protocol v4: length-prefixed binary frames on one bidirectional
//! stream, plus unframed check/verdict datagrams. Both directions encode
//! and decode here, so one definition answers for the replica, the
//! harnesses, and the conformance vectors the Go authority is held to.
//!
//! Every scalar is little-endian. The one exception is the frame's QUIC
//! variable-length integer length prefix, which is big-endian because
//! RFC 9000 §16 says so.

/// Client to server.
pub const K_HELLO: u8 = 1;
pub const K_INTENT: u8 = 2;
pub const K_RESYNC: u8 = 3;
/// Server to client.
pub const K_WELCOME: u8 = 16;
pub const K_EVENT: u8 = 17;
pub const K_REJECT: u8 = 18;
pub const K_SNAPSHOT: u8 = 19;

pub const DG_CHECK: u8 = 1;
pub const DG_VERDICT: u8 = 2;

pub const PROTO: u16 = 4;

pub const HELLO_HEADER: usize = 20;
pub const INTENT_HEADER: usize = 12;
pub const RESYNC_BYTES: usize = 8;
pub const WELCOME_HEADER: usize = 34;
pub const EVENT_HEADER: usize = 28;
pub const REJECT_BYTES: usize = 12;
pub const SNAPSHOT_HEADER: usize = 40;
pub const CHECK_BYTES: usize = 25;
pub const VERDICT_BYTES: usize = 34;

/// The checked tick was still inside the authority's hash ring.
pub const VERDICT_KNOWN: u8 = 1;
/// The hashes agreed — meaningful only alongside `VERDICT_KNOWN`.
pub const VERDICT_OK: u8 = 1 << 1;

pub const VARINT_MAX: u64 = (1 << 62) - 1;

/// The u32 the session ABI carries for a module word: the little-endian
/// load of the four wire bytes, so `module_word(w).to_le_bytes() == w`.
pub fn module_word(bytes: [u8; 4]) -> u32 {
    u32::from_le_bytes(bytes)
}

pub fn varint_len(v: u64) -> usize {
    match v {
        0..=0x3F => 1,
        0x40..=0x3FFF => 2,
        0x4000..=0x3FFF_FFFF => 4,
        _ => 8,
    }
}

/// Writes the shortest form of `v`, returning its byte length.
pub fn put_varint(dst: &mut [u8], v: u64) -> usize {
    match varint_len(v) {
        1 => {
            dst[0] = v as u8;
            1
        }
        2 => {
            dst[..2].copy_from_slice(&((v as u16) | 0x4000).to_be_bytes());
            2
        }
        4 => {
            dst[..4].copy_from_slice(&((v as u32) | 0x8000_0000).to_be_bytes());
            4
        }
        _ => {
            dst[..8].copy_from_slice(&(v | (3 << 62)).to_be_bytes());
            8
        }
    }
}

/// Reads a varint, or `None` when fewer than its encoded length remain —
/// longer-than-canonical forms decode fine, as RFC 9000 permits.
pub fn get_varint(src: &[u8]) -> Option<(u64, usize)> {
    let first = *src.first()?;
    let len = 1usize << (first >> 6);
    if src.len() < len {
        return None;
    }
    let mut b = [0u8; 8];
    b[8 - len..].copy_from_slice(&src[..len]);
    let v = u64::from_be_bytes(b) & (!0u64 >> (64 - 6 - 8 * (len as u32 - 1)));
    Some((v, len))
}

fn u16le(b: &[u8], at: usize) -> u16 {
    u16::from_le_bytes([b[at], b[at + 1]])
}

fn u32le(b: &[u8], at: usize) -> u32 {
    u32::from_le_bytes([b[at], b[at + 1], b[at + 2], b[at + 3]])
}

fn u64le(b: &[u8], at: usize) -> u64 {
    let mut v = [0u8; 8];
    v.copy_from_slice(&b[at..at + 8]);
    u64::from_le_bytes(v)
}

/// Locates the first whole frame in `buf`: `(kind, payload offset, payload
/// length, total frame length)`. `Ok(None)` means "need more bytes";
/// `Err(())` means the stream is unreadable and the connection is done.
#[allow(clippy::type_complexity)]
pub fn frame_bounds(buf: &[u8]) -> Result<Option<(u8, usize, usize, usize)>, ()> {
    let Some((qlen, pre)) = get_varint(buf) else {
        return Ok(None);
    };
    if qlen == 0 || qlen > VARINT_MAX {
        return Err(());
    }
    let total = pre + qlen as usize;
    if buf.len() < total {
        return Ok(None);
    }
    Ok(Some((buf[pre], pre + 1, qlen as usize - 1, total)))
}

/// Writes the length prefix and kind, returning `(payload offset, total)`.
fn frame(dst: &mut [u8], kind: u8, payload_len: usize) -> (usize, usize) {
    let body = 1 + payload_len;
    let pre = put_varint(dst, body as u64);
    dst[pre] = kind;
    (pre + 1, pre + body)
}

pub fn encode_hello(dst: &mut [u8], since_seq: i64, since_tick: u64, ticket: &[u8]) -> usize {
    let (at, total) = frame(dst, K_HELLO, HELLO_HEADER + ticket.len());
    dst[at..at + 2].copy_from_slice(&PROTO.to_le_bytes());
    dst[at + 2..at + 10].copy_from_slice(&since_seq.to_le_bytes());
    dst[at + 10..at + 18].copy_from_slice(&since_tick.to_le_bytes());
    dst[at + 18..at + 20].copy_from_slice(&(ticket.len() as u16).to_le_bytes());
    dst[at + 20..at + 20 + ticket.len()].copy_from_slice(ticket);
    total
}

pub fn encode_intent(dst: &mut [u8], id: u64, kind: u16, p: &[u8]) -> usize {
    let (at, total) = frame(dst, K_INTENT, INTENT_HEADER + p.len());
    dst[at..at + 8].copy_from_slice(&id.to_le_bytes());
    dst[at + 8..at + 10].copy_from_slice(&kind.to_le_bytes());
    dst[at + 10..at + 12].copy_from_slice(&(p.len() as u16).to_le_bytes());
    dst[at + 12..at + 12 + p.len()].copy_from_slice(p);
    total
}

pub fn encode_resync(dst: &mut [u8], have_seq: i64) -> usize {
    let (at, total) = frame(dst, K_RESYNC, RESYNC_BYTES);
    dst[at..at + 8].copy_from_slice(&have_seq.to_le_bytes());
    total
}

pub fn encode_welcome(dst: &mut [u8], w: &Welcome) -> usize {
    let (at, total) = frame(dst, K_WELCOME, WELCOME_HEADER + w.park.len());
    dst[at..at + 4].copy_from_slice(&w.epoch.to_le_bytes());
    dst[at + 4..at + 12].copy_from_slice(&w.seq.to_le_bytes());
    dst[at + 12..at + 20].copy_from_slice(&w.tick.to_le_bytes());
    dst[at + 20..at + 24].copy_from_slice(&w.hz.to_le_bytes());
    dst[at + 24] = w.role;
    dst[at + 25..at + 33].copy_from_slice(&w.terrain.to_le_bytes());
    dst[at + 33] = w.park.len() as u8;
    dst[at + 34..at + 34 + w.park.len()].copy_from_slice(w.park);
    total
}

pub fn encode_event(dst: &mut [u8], e: &Event) -> usize {
    let (at, total) = frame(dst, K_EVENT, EVENT_HEADER + e.p.len());
    dst[at..at + 8].copy_from_slice(&e.seq.to_le_bytes());
    dst[at + 8..at + 16].copy_from_slice(&e.tick.to_le_bytes());
    dst[at + 16..at + 18].copy_from_slice(&e.kind.to_le_bytes());
    dst[at + 18..at + 26].copy_from_slice(&e.intent.to_le_bytes());
    dst[at + 26..at + 28].copy_from_slice(&(e.p.len() as u16).to_le_bytes());
    dst[at + 28..at + 28 + e.p.len()].copy_from_slice(e.p);
    total
}

pub fn encode_reject(dst: &mut [u8], intent: u64, reason: u32) -> usize {
    let (at, total) = frame(dst, K_REJECT, REJECT_BYTES);
    dst[at..at + 8].copy_from_slice(&intent.to_le_bytes());
    dst[at + 8..at + 12].copy_from_slice(&reason.to_le_bytes());
    total
}

pub fn encode_snapshot(dst: &mut [u8], s: &Snapshot) -> usize {
    let (at, total) = frame(dst, K_SNAPSHOT, SNAPSHOT_HEADER + s.z.len());
    dst[at..at + 8].copy_from_slice(&s.seq.to_le_bytes());
    dst[at + 8..at + 16].copy_from_slice(&s.tick.to_le_bytes());
    dst[at + 16..at + 20].copy_from_slice(&s.epoch.to_le_bytes());
    dst[at + 20..at + 28].copy_from_slice(&s.wh.to_le_bytes());
    dst[at + 28..at + 36].copy_from_slice(&s.terrain.to_le_bytes());
    dst[at + 36..at + 40].copy_from_slice(&(s.z.len() as u32).to_le_bytes());
    dst[at + 40..at + 40 + s.z.len()].copy_from_slice(s.z);
    total
}

pub fn encode_check(dst: &mut [u8], tick: u64, wh: u64, ct_ms: u64) -> usize {
    dst[0] = DG_CHECK;
    dst[1..9].copy_from_slice(&tick.to_le_bytes());
    dst[9..17].copy_from_slice(&wh.to_le_bytes());
    dst[17..25].copy_from_slice(&ct_ms.to_le_bytes());
    CHECK_BYTES
}

pub fn encode_verdict(dst: &mut [u8], v: &Verdict) -> usize {
    dst[0] = DG_VERDICT;
    dst[1..9].copy_from_slice(&v.tick.to_le_bytes());
    dst[9..17].copy_from_slice(&v.now.to_le_bytes());
    dst[17..25].copy_from_slice(&v.ct_ms.to_le_bytes());
    dst[25] = v.flags;
    dst[26..30].copy_from_slice(&v.cw);
    dst[30..34].copy_from_slice(&v.pw);
    VERDICT_BYTES
}

pub struct Hello<'a> {
    pub proto: u16,
    pub since_seq: i64,
    pub since_tick: u64,
    pub ticket: &'a [u8],
}

pub struct Intent<'a> {
    pub id: u64,
    pub kind: u16,
    pub p: &'a [u8],
}

pub struct Welcome<'a> {
    pub epoch: u32,
    pub seq: i64,
    pub tick: u64,
    pub hz: u32,
    pub role: u8,
    pub terrain: u64,
    pub park: &'a [u8],
}

pub struct Event<'a> {
    pub seq: i64,
    pub tick: u64,
    pub kind: u16,
    pub intent: u64,
    pub p: &'a [u8],
}

pub struct Reject {
    pub intent: u64,
    pub reason: u32,
}

pub struct Snapshot<'a> {
    pub seq: i64,
    pub tick: u64,
    pub epoch: u32,
    pub wh: u64,
    pub terrain: u64,
    pub z: &'a [u8],
}

pub struct Check {
    pub tick: u64,
    pub wh: u64,
    pub ct_ms: u64,
}

pub struct Verdict {
    pub tick: u64,
    pub now: u64,
    pub ct_ms: u64,
    pub flags: u8,
    /// The client and park module sha256 prefixes, verbatim and in display
    /// order: hexing these left to right reproduces what `/wt-info` shows.
    /// Only the session ABI turns one into a number, and only ever as the
    /// little-endian load of these same four bytes.
    pub cw: [u8; 4],
    pub pw: [u8; 4],
}

pub fn parse_hello(p: &[u8]) -> Option<Hello<'_>> {
    if p.len() < HELLO_HEADER {
        return None;
    }
    let tlen = u16le(p, 18) as usize;
    if p.len() != HELLO_HEADER + tlen {
        return None;
    }
    Some(Hello {
        proto: u16le(p, 0),
        since_seq: u64le(p, 2) as i64,
        since_tick: u64le(p, 10),
        ticket: &p[20..20 + tlen],
    })
}

pub fn parse_intent(p: &[u8]) -> Option<Intent<'_>> {
    if p.len() < INTENT_HEADER {
        return None;
    }
    let plen = u16le(p, 10) as usize;
    if p.len() != INTENT_HEADER + plen {
        return None;
    }
    Some(Intent {
        id: u64le(p, 0),
        kind: u16le(p, 8),
        p: &p[12..12 + plen],
    })
}

pub fn parse_resync(p: &[u8]) -> Option<i64> {
    (p.len() == RESYNC_BYTES).then(|| u64le(p, 0) as i64)
}

pub fn parse_welcome(p: &[u8]) -> Option<Welcome<'_>> {
    if p.len() < WELCOME_HEADER {
        return None;
    }
    let nlen = p[33] as usize;
    if p.len() != WELCOME_HEADER + nlen {
        return None;
    }
    Some(Welcome {
        epoch: u32le(p, 0),
        seq: u64le(p, 4) as i64,
        tick: u64le(p, 12),
        hz: u32le(p, 20),
        role: p[24],
        terrain: u64le(p, 25),
        park: &p[34..34 + nlen],
    })
}

pub fn parse_event(p: &[u8]) -> Option<Event<'_>> {
    if p.len() < EVENT_HEADER {
        return None;
    }
    let plen = u16le(p, 26) as usize;
    if p.len() != EVENT_HEADER + plen {
        return None;
    }
    Some(Event {
        seq: u64le(p, 0) as i64,
        tick: u64le(p, 8),
        kind: u16le(p, 16),
        intent: u64le(p, 18),
        p: &p[28..28 + plen],
    })
}

pub fn parse_reject(p: &[u8]) -> Option<Reject> {
    (p.len() == REJECT_BYTES).then(|| Reject {
        intent: u64le(p, 0),
        reason: u32le(p, 8),
    })
}

pub fn parse_snapshot(p: &[u8]) -> Option<Snapshot<'_>> {
    if p.len() < SNAPSHOT_HEADER {
        return None;
    }
    let zlen = u32le(p, 36) as usize;
    if p.len() != SNAPSHOT_HEADER + zlen {
        return None;
    }
    Some(Snapshot {
        seq: u64le(p, 0) as i64,
        tick: u64le(p, 8),
        epoch: u32le(p, 16),
        wh: u64le(p, 20),
        terrain: u64le(p, 28),
        z: &p[40..40 + zlen],
    })
}

pub fn parse_check(b: &[u8]) -> Option<Check> {
    if b.len() != CHECK_BYTES || b[0] != DG_CHECK {
        return None;
    }
    Some(Check {
        tick: u64le(b, 1),
        wh: u64le(b, 9),
        ct_ms: u64le(b, 17),
    })
}

pub fn parse_verdict(b: &[u8]) -> Option<Verdict> {
    if b.len() != VERDICT_BYTES || b[0] != DG_VERDICT {
        return None;
    }
    Some(Verdict {
        tick: u64le(b, 1),
        now: u64le(b, 9),
        ct_ms: u64le(b, 17),
        flags: b[25],
        cw: [b[26], b[27], b[28], b[29]],
        pw: [b[30], b[31], b[32], b[33]],
    })
}
