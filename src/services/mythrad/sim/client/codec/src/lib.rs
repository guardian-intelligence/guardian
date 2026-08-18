//! chunkies wire protocol v5: length-prefixed binary frames on a
//! subscription stream, fixed-layout datagrams beside it. Every scalar is
//! little-endian; the one exception is the frame's QUIC variable-length
//! integer length prefix, which is big-endian because RFC 9000 §16 says
//! so.
//!
//! The protocol is game-blind. Its one shared unit is the EventRecord —
//! `intent u64 | elen u16 | SimEvent`, with SimEvent =
//! `kind u16 | actor u64 | payload` — the same bytes serving as the intent
//! envelope, the tick batch element, and (authority-side) the write-ahead
//! record element. Decoders reject non-canonical input rather than
//! normalize it: every structurally valid message has exactly one byte
//! representation, which is what makes verbatim forwarding safe.
//!
//! Three implementations — this crate, the Go codec package, and the TS
//! testkit mirror — are held to the shared golden vectors in
//! `src/services/mythrad/codec/spec/vectors.txt`. The vectors are the
//! spec; the implementations are not.

#![cfg_attr(not(test), no_std)]

/// Client to server.
pub const K_HELLO: u8 = 1;
pub const K_INTENT: u8 = 2;
pub const K_RESYNC: u8 = 3;
/// Server to client. 17 was v4's per-event kind and stays retired: a tag
/// never changes its layout, it dies with it.
pub const K_WELCOME: u8 = 16;
pub const K_REJECT: u8 = 18;
pub const K_SNAPSHOT: u8 = 19;
pub const K_TICK: u8 = 20;

pub const DG_CHECK: u8 = 1;
pub const DG_VERDICT: u8 = 2;

pub const PROTO: u16 = 5;

/// One frame body (kind byte plus payload) in every implementation —
/// spec/caps.txt is the source of truth, and the conformance tests hold
/// this constant to it. The v4 era shipped this cap Go-only; a hostile
/// peer could declare a 4GiB frame to the Rust side.
pub const MAX_FRAME: usize = 128 * 1024;

/// The codec's contract for the datagram class: fixed layouts stay far
/// below it, any future sample-class datagram is bounded by it statically.
/// 1200 is the QUIC minimum path MTU floor; measuring the real budget is
/// the transport's job.
pub const DG_MAX: usize = 1200;

pub const HELLO_HEADER: usize = 24;
pub const RESYNC_BYTES: usize = 12;
pub const WELCOME_HEADER: usize = 46;
pub const TICK_HEADER: usize = 18;
pub const REJECT_BYTES: usize = 12;
pub const SNAPSHOT_HEADER: usize = 44;
pub const CHECK_BYTES: usize = 29;
pub const VERDICT_BYTES: usize = 42;

/// EventRecord: intent u64, elen u16, then elen bytes of SimEvent.
/// SimEvent: kind u16, actor u64, then the game payload.
pub const EVENT_RECORD_HEADER: usize = 10;
pub const SIM_EVENT_HEADER: usize = 10;

/// The reserved intent id for authority-minted records. A client-authored
/// intent must carry a nonzero id — it is the dedup and ack handle — so
/// the intent-frame parser refuses zero while tick batches accept it.
pub const SYSTEM_INTENT: u64 = 0;

/// Where the actor u64 sits in an intent frame's payload: the gateway's
/// game-blind binding check reads it here and forwards the bytes verbatim.
pub const INTENT_ACTOR_OFFSET: usize = EVENT_RECORD_HEADER + 2;

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
/// `Err(())` means the peer has lost the framing and the connection is
/// done — including a declared body over `MAX_FRAME`, which no honest
/// peer sends.
#[allow(clippy::type_complexity)]
pub fn frame_bounds(buf: &[u8]) -> Result<Option<(u8, usize, usize, usize)>, ()> {
    let Some((qlen, pre)) = get_varint(buf) else {
        return Ok(None);
    };
    if qlen == 0 || qlen > MAX_FRAME as u64 {
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

// ---------- the EventRecord ----------

/// The one unit the protocol shares across contexts. `sim_event` aliases
/// the trailing `kind | actor | payload` bytes — exactly what a
/// simulation's apply receives, with no re-framing between wire and ABI.
pub struct EventRecord<'a> {
    pub intent: u64,
    pub kind: u16,
    pub actor: u64,
    pub payload: &'a [u8],
    pub sim_event: &'a [u8],
}

/// Writes one record at the front of `dst`, returning its length.
pub fn put_record(dst: &mut [u8], intent: u64, kind: u16, actor: u64, p: &[u8]) -> usize {
    let elen = SIM_EVENT_HEADER + p.len();
    dst[..8].copy_from_slice(&intent.to_le_bytes());
    dst[8..10].copy_from_slice(&(elen as u16).to_le_bytes());
    dst[10..12].copy_from_slice(&kind.to_le_bytes());
    dst[12..20].copy_from_slice(&actor.to_le_bytes());
    dst[20..20 + p.len()].copy_from_slice(p);
    EVENT_RECORD_HEADER + elen
}

/// Reads one record from the front of `b`, with the bytes consumed. `None`
/// when `b` does not begin with a whole, well-formed record.
pub fn parse_record(b: &[u8]) -> Option<(EventRecord<'_>, usize)> {
    if b.len() < EVENT_RECORD_HEADER {
        return None;
    }
    let elen = u16le(b, 8) as usize;
    let total = EVENT_RECORD_HEADER + elen;
    if elen < SIM_EVENT_HEADER || b.len() < total {
        return None;
    }
    let ev = &b[EVENT_RECORD_HEADER..total];
    Some((
        EventRecord {
            intent: u64le(b, 0),
            kind: u16le(ev, 0),
            actor: u64le(ev, 2),
            payload: &ev[SIM_EVENT_HEADER..],
            sim_event: ev,
        },
        total,
    ))
}

/// Validates a run of exactly `count` records covering all of `b`. After
/// this succeeds, `Records` iteration over the same bytes is infallible.
pub fn validate_records(b: &[u8], count: usize) -> bool {
    let mut rest = b;
    let mut seen = 0usize;
    while seen < count {
        let Some((_, n)) = parse_record(rest) else {
            return false;
        };
        rest = &rest[n..];
        seen += 1;
    }
    rest.is_empty()
}

/// Iterates a validated record run.
pub struct Records<'a> {
    rest: &'a [u8],
}

impl<'a> Iterator for Records<'a> {
    type Item = EventRecord<'a>;
    fn next(&mut self) -> Option<EventRecord<'a>> {
        let (rec, n) = parse_record(self.rest)?;
        self.rest = &self.rest[n..];
        Some(rec)
    }
}

// ---------- encoders ----------

pub fn encode_hello(
    dst: &mut [u8],
    since_lineage: u32,
    since_seq: i64,
    since_tick: u64,
    ticket: &[u8],
) -> usize {
    let (at, total) = frame(dst, K_HELLO, HELLO_HEADER + ticket.len());
    dst[at..at + 2].copy_from_slice(&PROTO.to_le_bytes());
    dst[at + 2..at + 6].copy_from_slice(&since_lineage.to_le_bytes());
    dst[at + 6..at + 14].copy_from_slice(&since_seq.to_le_bytes());
    dst[at + 14..at + 22].copy_from_slice(&since_tick.to_le_bytes());
    dst[at + 22..at + 24].copy_from_slice(&(ticket.len() as u16).to_le_bytes());
    dst[at + 24..at + 24 + ticket.len()].copy_from_slice(ticket);
    total
}

pub fn encode_intent(dst: &mut [u8], intent: u64, kind: u16, actor: u64, p: &[u8]) -> usize {
    let rec = EVENT_RECORD_HEADER + SIM_EVENT_HEADER + p.len();
    let (at, total) = frame(dst, K_INTENT, rec);
    put_record(&mut dst[at..], intent, kind, actor, p);
    total
}

pub fn encode_resync(dst: &mut [u8], lineage: u32, have_seq: i64) -> usize {
    let (at, total) = frame(dst, K_RESYNC, RESYNC_BYTES);
    dst[at..at + 4].copy_from_slice(&lineage.to_le_bytes());
    dst[at + 4..at + 12].copy_from_slice(&have_seq.to_le_bytes());
    total
}

pub fn encode_welcome(dst: &mut [u8], w: &Welcome) -> usize {
    // The name's length rides in a u8, so it clamps rather than wrapping:
    // a truncated name is a readable frame, a wrapped one is not.
    let chunk = &w.chunk[..w.chunk.len().min(u8::MAX as usize)];
    let (at, total) = frame(dst, K_WELCOME, WELCOME_HEADER + chunk.len());
    dst[at..at + 4].copy_from_slice(&w.lineage.to_le_bytes());
    dst[at + 4..at + 8].copy_from_slice(&w.generation.to_le_bytes());
    dst[at + 8..at + 12].copy_from_slice(&w.sub.to_le_bytes());
    dst[at + 12..at + 16].copy_from_slice(&w.epoch.to_le_bytes());
    dst[at + 16..at + 24].copy_from_slice(&w.seq.to_le_bytes());
    dst[at + 24..at + 32].copy_from_slice(&w.tick.to_le_bytes());
    dst[at + 32..at + 36].copy_from_slice(&w.hz.to_le_bytes());
    dst[at + 36] = w.role;
    dst[at + 37..at + 45].copy_from_slice(&w.content.to_le_bytes());
    dst[at + 45] = chunk.len() as u8;
    dst[at + 46..at + 46 + chunk.len()].copy_from_slice(chunk);
    total
}

/// Frames a tick batch around an already-encoded record run.
pub fn encode_tick(dst: &mut [u8], tick: u64, first_seq: i64, count: u16, records: &[u8]) -> usize {
    let (at, total) = frame(dst, K_TICK, TICK_HEADER + records.len());
    dst[at..at + 8].copy_from_slice(&tick.to_le_bytes());
    dst[at + 8..at + 16].copy_from_slice(&first_seq.to_le_bytes());
    dst[at + 16..at + 18].copy_from_slice(&count.to_le_bytes());
    dst[at + 18..at + 18 + records.len()].copy_from_slice(records);
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
    dst[at..at + 4].copy_from_slice(&s.lineage.to_le_bytes());
    dst[at + 4..at + 12].copy_from_slice(&s.seq.to_le_bytes());
    dst[at + 12..at + 20].copy_from_slice(&s.tick.to_le_bytes());
    dst[at + 20..at + 24].copy_from_slice(&s.epoch.to_le_bytes());
    dst[at + 24..at + 32].copy_from_slice(&s.wh.to_le_bytes());
    dst[at + 32..at + 40].copy_from_slice(&s.content.to_le_bytes());
    dst[at + 40..at + 44].copy_from_slice(&(s.z.len() as u32).to_le_bytes());
    dst[at + 44..at + 44 + s.z.len()].copy_from_slice(s.z);
    total
}

pub fn encode_check(dst: &mut [u8], sub: u32, tick: u64, wh: u64, ct_ms: u64) -> usize {
    dst[0] = DG_CHECK;
    dst[1..5].copy_from_slice(&sub.to_le_bytes());
    dst[5..13].copy_from_slice(&tick.to_le_bytes());
    dst[13..21].copy_from_slice(&wh.to_le_bytes());
    dst[21..29].copy_from_slice(&ct_ms.to_le_bytes());
    CHECK_BYTES
}

pub fn encode_verdict(dst: &mut [u8], v: &Verdict) -> usize {
    dst[0] = DG_VERDICT;
    dst[1..5].copy_from_slice(&v.sub.to_le_bytes());
    dst[5..9].copy_from_slice(&v.lineage.to_le_bytes());
    dst[9..17].copy_from_slice(&v.tick.to_le_bytes());
    dst[17..25].copy_from_slice(&v.now.to_le_bytes());
    dst[25..33].copy_from_slice(&v.ct_ms.to_le_bytes());
    dst[33] = v.flags;
    dst[34..38].copy_from_slice(&v.cw);
    dst[38..42].copy_from_slice(&v.pw);
    VERDICT_BYTES
}

// ---------- message types ----------

pub struct Hello<'a> {
    pub proto: u16,
    pub since_lineage: u32,
    pub since_seq: i64,
    pub since_tick: u64,
    pub ticket: &'a [u8],
}

/// Welcome opens every subscription. A lineage the client has already seen
/// continues; a new one means "discard what you saw and restore". `sub` is
/// this subscription's datagram tag.
pub struct Welcome<'a> {
    pub lineage: u32,
    pub generation: u32,
    pub sub: u32,
    pub epoch: u32,
    pub seq: i64,
    pub tick: u64,
    pub hz: u32,
    pub role: u8,
    pub content: u64,
    pub chunk: &'a [u8],
}

/// One authority tick's accepted records. Seqs are dense from `first_seq`
/// in record order. `records` is the validated encoded run.
pub struct Tick<'a> {
    pub tick: u64,
    pub first_seq: i64,
    pub count: u16,
    pub records: &'a [u8],
}

impl<'a> Tick<'a> {
    pub fn records(&self) -> Records<'a> {
        Records { rest: self.records }
    }
}

pub struct Reject {
    pub intent: u64,
    pub reason: u32,
}

pub struct Snapshot<'a> {
    pub lineage: u32,
    pub seq: i64,
    pub tick: u64,
    pub epoch: u32,
    pub wh: u64,
    pub content: u64,
    pub z: &'a [u8],
}

pub struct Check {
    pub sub: u32,
    pub tick: u64,
    pub wh: u64,
    pub ct_ms: u64,
}

pub struct Verdict {
    pub sub: u32,
    pub lineage: u32,
    pub tick: u64,
    pub now: u64,
    pub ct_ms: u64,
    pub flags: u8,
    /// The client and chunk module sha256 prefixes, verbatim and in
    /// display order: hexing these left to right reproduces the display
    /// form. Only the session ABI turns one into a number, and only ever
    /// as the little-endian load of these same four bytes.
    pub cw: [u8; 4],
    pub pw: [u8; 4],
}

// ---------- decoders ----------

pub fn parse_hello(p: &[u8]) -> Option<Hello<'_>> {
    if p.len() < HELLO_HEADER {
        return None;
    }
    let tlen = u16le(p, 22) as usize;
    if p.len() != HELLO_HEADER + tlen {
        return None;
    }
    Some(Hello {
        proto: u16le(p, 0),
        since_lineage: u32le(p, 2),
        since_seq: u64le(p, 6) as i64,
        since_tick: u64le(p, 14),
        ticket: &p[24..24 + tlen],
    })
}

/// An intent frame's payload: exactly one EventRecord with a nonzero
/// intent id (zero is the authority's to mint).
pub fn parse_intent(p: &[u8]) -> Option<EventRecord<'_>> {
    let (rec, n) = parse_record(p)?;
    if n != p.len() || rec.intent == SYSTEM_INTENT {
        return None;
    }
    Some(rec)
}

pub fn parse_resync(p: &[u8]) -> Option<(u32, i64)> {
    (p.len() == RESYNC_BYTES).then(|| (u32le(p, 0), u64le(p, 4) as i64))
}

pub fn parse_welcome(p: &[u8]) -> Option<Welcome<'_>> {
    if p.len() < WELCOME_HEADER {
        return None;
    }
    let nlen = p[45] as usize;
    if p.len() != WELCOME_HEADER + nlen {
        return None;
    }
    Some(Welcome {
        lineage: u32le(p, 0),
        generation: u32le(p, 4),
        sub: u32le(p, 8),
        epoch: u32le(p, 12),
        seq: u64le(p, 16) as i64,
        tick: u64le(p, 24),
        hz: u32le(p, 32),
        role: p[36],
        content: u64le(p, 37),
        chunk: &p[46..46 + nlen],
    })
}

pub fn parse_tick(p: &[u8]) -> Option<Tick<'_>> {
    if p.len() < TICK_HEADER {
        return None;
    }
    let count = u16le(p, 16);
    let records = &p[TICK_HEADER..];
    if !validate_records(records, count as usize) {
        return None;
    }
    Some(Tick {
        tick: u64le(p, 0),
        first_seq: u64le(p, 8) as i64,
        count,
        records,
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
    let zlen = u32le(p, 40) as usize;
    if p.len() != SNAPSHOT_HEADER + zlen {
        return None;
    }
    Some(Snapshot {
        lineage: u32le(p, 0),
        seq: u64le(p, 4) as i64,
        tick: u64le(p, 12),
        epoch: u32le(p, 20),
        wh: u64le(p, 24),
        content: u64le(p, 32),
        z: &p[44..44 + zlen],
    })
}

pub fn parse_check(b: &[u8]) -> Option<Check> {
    if b.len() != CHECK_BYTES || b[0] != DG_CHECK {
        return None;
    }
    Some(Check {
        sub: u32le(b, 1),
        tick: u64le(b, 5),
        wh: u64le(b, 13),
        ct_ms: u64le(b, 21),
    })
}

pub fn parse_verdict(b: &[u8]) -> Option<Verdict> {
    if b.len() != VERDICT_BYTES || b[0] != DG_VERDICT {
        return None;
    }
    Some(Verdict {
        sub: u32le(b, 1),
        lineage: u32le(b, 5),
        tick: u64le(b, 9),
        now: u64le(b, 17),
        ct_ms: u64le(b, 25),
        flags: b[33],
        cw: [b[34], b[35], b[36], b[37]],
        pw: [b[38], b[39], b[40], b[41]],
    })
}

#[cfg(test)]
mod tests;
