use super::*;
use std::sync::{Mutex, MutexGuard};

// The core is a single static in the module that links it, so tests
// serialize on this lock and re-init in every case.
static LOCK: Mutex<()> = Mutex::new(());
static mut SESSION: Session = Session::NEW;

const NONCE: u32 = 0xABCD_1234;
const DOG: u64 = 0x0BAD_C0DE_DEAD_BEEF;
const TERRAIN: u64 = 0x1122_3344_5566_7788;
const HZ: u64 = 24;
const PARK_A: u32 = 0x1111_1111;
const PARK_B: u32 = 0x2222_2222;
/// Park ABI values the core does not name for itself, so the mock can
/// stand in for the two events that move the world sideways.
const EV_CLOCK_SKIP: u16 = 9;
const ERR_TICK: u32 = 11;

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
enum Verb {
    Apply(u16),
    Step,
    Snapshot,
    Restore,
}

// A recording stand-in for the park module: enough state that rollback,
// restore, and hashing mean something (tick plus a fold of every applied
// event), and no game rules at all.
#[derive(Clone, Copy)]
struct Toy {
    tick: u64,
    acc: u64,
    /// Edits already standing in this world. The park does not mutate on
    /// a repeat — a second move_to names the same target, a second boost
    /// or check-in is refused — so a replayed event lands on the same
    /// state twice without moving it twice.
    seen: [u64; 8],
    seen_len: usize,
}

const TOY_BYTES: usize = 18;

/// The payload `intent_move_to` puts on the wire, which is also what the
/// journal echoes back when it answers.
fn move_payload(node: u16) -> [u8; 10] {
    let mut p = [0u8; 10];
    p[..8].copy_from_slice(&DOG.to_le_bytes());
    p[8..].copy_from_slice(&node.to_le_bytes());
    p
}

fn toy_state(tick: u64, acc: u64) -> [u8; TOY_BYTES] {
    let mut b = [0u8; TOY_BYTES];
    b[..2].copy_from_slice(b"TP");
    b[2..10].copy_from_slice(&tick.to_le_bytes());
    b[10..18].copy_from_slice(&acc.to_le_bytes());
    b
}

/// One event's contribution to the toy world, as both the mock park and
/// the reference timeline compute it.
fn edit_of(ev: &[u8]) -> u64 {
    toy_hash(
        u16::from_le_bytes([ev[0], ev[1]]) as u64,
        ev.iter()
            .fold(0u64, |a, &b| a.wrapping_mul(31).wrapping_add(b as u64)),
    )
}

fn toy_hash(tick: u64, acc: u64) -> u64 {
    let mut z = tick
        .wrapping_mul(0x9E37_79B9_7F4A_7C15)
        .wrapping_add(acc.wrapping_mul(0xBF58_476D_1CE4_E5B9));
    z = (z ^ (z >> 30)).wrapping_mul(0x94D0_49BB_1331_11EB);
    z ^ (z >> 31)
}

struct Mock {
    park: Toy,
    verbs: Vec<Verb>,
    sent: Vec<Vec<u8>>,
    dgs: Vec<Vec<u8>>,
    reqs: Vec<(u32, u64)>,
    emits: Vec<(u32, u64, u64)>,
    restore_code: u32,
    reject: Option<(u16, u32)>,
}

impl Mock {
    fn new() -> Mock {
        Mock {
            park: Toy {
                tick: 0,
                acc: 0,
                seen: [0; 8],
                seen_len: 0,
            },
            verbs: Vec::new(),
            sent: Vec::new(),
            dgs: Vec::new(),
            reqs: Vec::new(),
            emits: Vec::new(),
            restore_code: 0,
            reject: None,
        }
    }

    fn clear(&mut self) {
        self.verbs.clear();
        self.sent.clear();
        self.dgs.clear();
        self.reqs.clear();
        self.emits.clear();
    }

    fn frame_kinds(&self) -> Vec<u8> {
        self.sent
            .iter()
            .map(|f| wire::frame_bounds(f).unwrap().unwrap().0)
            .collect()
    }

    fn intents(&self) -> Vec<(u64, u16)> {
        self.sent
            .iter()
            .filter_map(|f| {
                let (kind, off, len, _) = wire::frame_bounds(f).unwrap().unwrap();
                (kind == wire::K_INTENT).then(|| {
                    let i = wire::parse_intent(&f[off..off + len]).unwrap();
                    (i.id, i.kind)
                })
            })
            .collect()
    }

    fn checks(&self) -> Vec<(u64, u64)> {
        self.dgs
            .iter()
            .filter_map(|d| wire::parse_check(d).map(|c| (c.tick, c.wh)))
            .collect()
    }

    fn emits_of(&self, kind: u32) -> Vec<(u64, u64)> {
        self.emits
            .iter()
            .filter(|e| e.0 == kind)
            .map(|e| (e.1, e.2))
            .collect()
    }

    fn reqs_of(&self, kind: u32) -> Vec<u64> {
        self.reqs
            .iter()
            .filter(|r| r.0 == kind)
            .map(|r| r.1)
            .collect()
    }

    fn applies(&self) -> Vec<u16> {
        self.verbs
            .iter()
            .filter_map(|v| match v {
                Verb::Apply(k) => Some(*k),
                _ => None,
            })
            .collect()
    }

    fn count(&self, want: Verb) -> usize {
        self.verbs.iter().filter(|v| **v == want).count()
    }
}

impl Host for Mock {
    fn park_apply(&mut self, ev: &[u8]) -> u32 {
        let kind = u16::from_le_bytes([ev[0], ev[1]]);
        self.verbs.push(Verb::Apply(kind));
        if let Some((k, code)) = self.reject
            && k == kind
        {
            return code;
        }
        // The one park event that is not an edit on top of the tick it
        // lands in: it moves the tick, with no step behind it, and only
        // ever forward.
        if kind == EV_CLOCK_SKIP && ev.len() == 10 {
            let mut t = [0u8; 8];
            t.copy_from_slice(&ev[2..10]);
            let to = u64::from_le_bytes(t);
            if to <= self.park.tick {
                return ERR_TICK;
            }
            self.park.tick = to;
            return 0;
        }
        let edit = edit_of(ev);
        let p = &mut self.park;
        if p.seen[..p.seen_len].contains(&edit) {
            return 0; // already standing; nothing to change
        }
        if p.seen_len < p.seen.len() {
            p.seen[p.seen_len] = edit;
            p.seen_len += 1;
        }
        // Order-independent, because the park's is: an edit lands on one
        // dog's record, so two edits to different dogs commute and the
        // world does not care which arrived first. A fold would make this
        // mock stricter than the thing it stands in for.
        p.acc ^= edit;
        0
    }

    fn park_step(&mut self) {
        self.verbs.push(Verb::Step);
        self.park.tick += 1;
    }

    fn park_snapshot(&mut self, dst: &mut [u8]) -> u32 {
        self.verbs.push(Verb::Snapshot);
        let p = self.park;
        dst[..TOY_BYTES].copy_from_slice(&toy_state(p.tick, p.acc));
        TOY_BYTES as u32
    }

    fn park_restore(&mut self, state: &[u8]) -> u32 {
        self.verbs.push(Verb::Restore);
        if self.restore_code != 0 {
            return self.restore_code;
        }
        if state.len() != TOY_BYTES || &state[..2] != b"TP" {
            return 1;
        }
        let mut t = [0u8; 8];
        let mut a = [0u8; 8];
        t.copy_from_slice(&state[2..10]);
        a.copy_from_slice(&state[10..18]);
        self.park = Toy {
            tick: u64::from_le_bytes(t),
            acc: u64::from_le_bytes(a),
            seen: [0; 8],
            seen_len: 0,
        };
        0
    }

    fn park_hash(&mut self) -> u64 {
        toy_hash(self.park.tick, self.park.acc)
    }

    fn park_tick(&mut self) -> u64 {
        self.park.tick
    }

    fn send_stream(&mut self, frame: &[u8]) {
        self.sent.push(frame.to_vec());
    }

    fn send_datagram(&mut self, datagram: &[u8]) {
        self.dgs.push(datagram.to_vec());
    }

    fn inflate(&mut self, src: &[u8], dst: &mut [u8]) -> u32 {
        // The host's contract: 0 for a failure, and for a result that
        // would not fit the destination.
        if src.len() > dst.len() {
            return 0;
        }
        dst[..src.len()].copy_from_slice(src);
        src.len() as u32
    }

    fn request(&mut self, kind: u32, a: u64) {
        self.reqs.push((kind, a));
    }

    fn emit(&mut self, kind: u32, a: u64, b: u64) {
        self.emits.push((kind, a, b));
    }
}

struct Rig {
    _g: MutexGuard<'static, ()>,
    s: &'static mut Session,
    m: Mock,
    now: u64,
    role: u32,
}

thread_local! {
    static RIG_LIVE: std::cell::Cell<bool> = const { std::cell::Cell::new(false) };
}

impl Drop for Rig {
    fn drop(&mut self) {
        RIG_LIVE.with(|f| f.set(false));
    }
}

impl Rig {
    fn new(role: u32) -> Rig {
        // Two rigs on one thread would deadlock on the lock below rather
        // than fail, and `let mut r = ...` twice in a scope does NOT drop
        // the first. Say so instead of hanging until the test times out.
        RIG_LIVE.with(|f| {
            assert!(
                !f.get(),
                "a Rig is already live on this thread: they share the module's \
                 statics, so scope one before building the next"
            );
            f.set(true);
        });
        let g = LOCK.lock().unwrap_or_else(|e| e.into_inner());
        let s = unsafe { &mut *(&raw mut SESSION) };
        s.init(DOG, role, 5000, NONCE, 0);
        Rig {
            _g: g,
            s,
            m: Mock::new(),
            now: 0,
            role,
        }
    }

    fn feed(&mut self, frame: &[u8]) {
        self.s.on_stream(&mut self.m, frame, self.now);
    }

    fn pump(&mut self) -> u32 {
        self.s.pump(&mut self.m, self.now, 8000)
    }

    fn advance(&mut self, ms: u64) {
        for _ in 0..ms / 16 {
            self.now += 16;
            self.pump();
        }
    }

    fn advance_starved(&mut self, ms: u64) -> Vec<u32> {
        let mut states = Vec::new();
        for _ in 0..ms / 16 {
            self.now += 16;
            states.push((self.s.pump(&mut self.m, self.now, 0) >> S_CLOCK_SHIFT) & 3);
        }
        states
    }

    /// The journal answers the join this connection sent, which is what
    /// tells the core its dog is in the park.
    fn confirm_join(&mut self) {
        let i = self
            .s
            .pending_kind(EV_JOIN)
            .expect("no join is waiting to be answered");
        let id = bufs().intents[i].id;
        let seq = self.s.seq() + 1;
        let tick = self.s.tick();
        self.event(seq, tick, EV_JOIN, id, &DOG.to_le_bytes());
        self.pump();
        assert!(self.s.presence, "the join was not acknowledged");
    }

    /// Pumps until the journal has been taken up to `seq`, and answers
    /// with the status of the pump that took it — the frame the host would
    /// have absorbed the event on.
    fn pump_until_seq(&mut self, seq: i64) -> u32 {
        for _ in 0..400 {
            self.now += 16;
            let flags = self.pump();
            if self.s.seq() >= seq {
                return flags;
            }
        }
        panic!("event never applied (at seq {})", self.s.seq());
    }

    fn run_to_tick(&mut self, tick: u64) {
        for _ in 0..20_000 {
            if self.s.tick() >= tick {
                return;
            }
            self.now += 16;
            self.pump();
        }
        panic!("replica never reached tick {tick} (at {})", self.s.tick());
    }

    fn welcome(&mut self, tick: u64, terrain: u64) {
        let mut buf = [0u8; 128];
        let n = wire::encode_welcome(
            &mut buf,
            &wire::Welcome {
                epoch: 3,
                seq: 0,
                tick,
                hz: HZ as u32,
                role: self.role as u8,
                terrain,
                park: b"park-mythra",
            },
        );
        self.feed(&buf[..n]);
    }

    fn snapshot(&mut self, seq: i64, tick: u64, acc: u64, terrain: u64) {
        let mut buf = [0u8; 128];
        let n = wire::encode_snapshot(
            &mut buf,
            &wire::Snapshot {
                seq,
                tick,
                epoch: 3,
                wh: toy_hash(tick, acc),
                terrain,
                z: &toy_state(tick, acc),
            },
        );
        self.feed(&buf[..n]);
    }

    fn event(&mut self, seq: i64, tick: u64, kind: u16, intent: u64, p: &[u8]) {
        let mut buf = [0u8; 128];
        let n = wire::encode_event(
            &mut buf,
            &wire::Event {
                seq,
                tick,
                kind,
                intent,
                p,
            },
        );
        self.feed(&buf[..n]);
    }

    fn reject(&mut self, intent: u64, reason: u32) {
        let mut buf = [0u8; 32];
        let n = wire::encode_reject(&mut buf, intent, reason);
        self.feed(&buf[..n]);
    }

    /// A verdict naming both the checked tick and where the authority
    /// stood when it answered.
    fn verdict_at(&mut self, tick: u64, now: u64, known: bool, ok: bool) {
        let mut buf = [0u8; 64];
        let flags = (known as u8) | ((known && ok) as u8) << 1;
        let n = wire::encode_verdict(
            &mut buf,
            &wire::Verdict {
                tick,
                now,
                ct_ms: self.now,
                flags,
                cw: [0; 4],
                pw: [0; 4],
            },
        );
        let dg = buf[..n].to_vec();
        self.s.on_datagram(&mut self.m, &dg, self.now);
    }

    fn verdict(&mut self, tick: u64, known: bool, ok: bool, pw: u32) {
        let mut buf = [0u8; 64];
        let flags = (known as u8) | ((known && ok) as u8) << 1;
        let n = wire::encode_verdict(
            &mut buf,
            &wire::Verdict {
                tick,
                now: self.s.tick(),
                ct_ms: self.now,
                flags,
                cw: [0; 4],
                pw: pw.to_le_bytes(),
            },
        );
        let dg = buf[..n].to_vec();
        self.s.on_datagram(&mut self.m, &dg, self.now);
    }

    /// Connected, greeted, terrain loaded, world restored at `tick` — the
    /// host names the park module it instantiated before it dials.
    fn boot(role: u32, tick: u64) -> Rig {
        let mut r = Rig::new(role);
        r.s.module_swapped(&mut r.m, PARK_A);
        r.s.connected(&mut r.m, b"ticket-bytes", 0);
        r.welcome(tick, TERRAIN);
        r.s.terrain_ready(&mut r.m, TERRAIN, true);
        r.snapshot(100, tick, 7, TERRAIN);
        r.pump();
        assert!(r.s.have_state, "boot must land a world");
        r
    }
}

// ---- codec ----

mod codec {
    use super::super::wire::*;

    #[test]
    fn varints_round_trip_at_every_boundary() {
        let mut buf = [0u8; 8];
        let mut cases: Vec<u64> = Vec::new();
        for edge in [0u64, 0x3F, 0x40, 0x3FFF, 0x4000, 0x3FFF_FFFF, 0x4000_0000] {
            cases.push(edge);
        }
        cases.push(VARINT_MAX);
        for shift in 0..62 {
            cases.push(1u64 << shift);
            cases.push((1u64 << shift) - 1);
        }
        for v in cases {
            let n = put_varint(&mut buf, v);
            assert_eq!(n, varint_len(v), "length for {v}");
            let (got, used) = get_varint(&buf[..n]).expect("decodes");
            assert_eq!((got, used), (v, n), "round trip {v}");
            // one byte short is "wait for more", never a wrong value
            if n > 1 {
                assert!(get_varint(&buf[..n - 1]).is_none());
            }
        }
    }

    #[test]
    fn non_canonical_varint_forms_decode() {
        // RFC 9000 permits longer-than-needed encodings; the decoder takes
        // them, the encoder never emits them.
        let two = [0x40u8, 0x01];
        assert_eq!(get_varint(&two), Some((1, 2)));
        let four = [0x80u8, 0, 0, 1];
        assert_eq!(get_varint(&four), Some((1, 4)));
        let eight = [0xC0u8, 0, 0, 0, 0, 0, 0, 1];
        assert_eq!(get_varint(&eight), Some((1, 8)));
    }

    #[test]
    fn every_frame_round_trips() {
        let mut b = [0u8; 4096];
        let ticket = b"a-signed-admission-ticket";

        let n = encode_hello(&mut b, -1, 12_345, ticket);
        let (k, off, len, total) = frame_bounds(&b[..n]).unwrap().unwrap();
        assert_eq!((k, total), (K_HELLO, n));
        let h = parse_hello(&b[off..off + len]).unwrap();
        assert_eq!((h.proto, h.since_seq, h.since_tick), (PROTO, -1, 12_345));
        assert_eq!(h.ticket, ticket);

        for p in [&[][..], &[1, 2, 3, 4, 5, 6, 7, 8, 9, 10][..]] {
            let n = encode_intent(&mut b, u64::MAX, 8, p);
            let (k, off, len, total) = frame_bounds(&b[..n]).unwrap().unwrap();
            assert_eq!((k, total), (K_INTENT, n));
            let i = parse_intent(&b[off..off + len]).unwrap();
            assert_eq!((i.id, i.kind, i.p), (u64::MAX, 8, p));
        }

        let n = encode_resync(&mut b, i64::MIN);
        let (k, off, len, _) = frame_bounds(&b[..n]).unwrap().unwrap();
        assert_eq!(k, K_RESYNC);
        assert_eq!(parse_resync(&b[off..off + len]), Some(i64::MIN));

        let w = Welcome {
            epoch: 7,
            seq: 1 << 40,
            tick: 999_999,
            hz: 120,
            role: 1,
            terrain: 0xDEAD_BEEF_CAFE_F00D,
            park: b"park-mythra",
        };
        let n = encode_welcome(&mut b, &w);
        let (k, off, len, _) = frame_bounds(&b[..n]).unwrap().unwrap();
        assert_eq!(k, K_WELCOME);
        let g = parse_welcome(&b[off..off + len]).unwrap();
        assert_eq!(
            (g.epoch, g.seq, g.tick, g.hz, g.role, g.terrain, g.park),
            (w.epoch, w.seq, w.tick, w.hz, w.role, w.terrain, w.park)
        );

        let e = Event {
            seq: 4242,
            tick: 1 << 40,
            kind: 4,
            intent: 0x0000_ABCD_0000_0001,
            p: &[9u8; 10],
        };
        let n = encode_event(&mut b, &e);
        let (k, off, len, _) = frame_bounds(&b[..n]).unwrap().unwrap();
        assert_eq!(k, K_EVENT);
        let g = parse_event(&b[off..off + len]).unwrap();
        assert_eq!(
            (g.seq, g.tick, g.kind, g.intent, g.p),
            (e.seq, e.tick, e.kind, e.intent, e.p)
        );

        let n = encode_reject(&mut b, 77, 10);
        let (k, off, len, _) = frame_bounds(&b[..n]).unwrap().unwrap();
        assert_eq!(k, K_REJECT);
        let g = parse_reject(&b[off..off + len]).unwrap();
        assert_eq!((g.intent, g.reason), (77, 10));

        // a payload past the one-byte varint boundary: the prefix grows
        let z = [0x5Au8; 300];
        let s = Snapshot {
            seq: 5,
            tick: 6,
            epoch: 7,
            wh: 8,
            terrain: 9,
            z: &z,
        };
        let n = encode_snapshot(&mut b, &s);
        assert_eq!(n, 2 + 1 + SNAPSHOT_HEADER + z.len());
        let (k, off, len, total) = frame_bounds(&b[..n]).unwrap().unwrap();
        assert_eq!((k, total), (K_SNAPSHOT, n));
        let g = parse_snapshot(&b[off..off + len]).unwrap();
        assert_eq!(
            (g.seq, g.tick, g.epoch, g.wh, g.terrain, g.z),
            (s.seq, s.tick, s.epoch, s.wh, s.terrain, s.z)
        );
    }

    #[test]
    fn datagrams_round_trip() {
        let mut b = [0u8; 64];
        let n = encode_check(&mut b, 1 << 33, 0xFEED_FACE_1234_5678, 999);
        assert_eq!(n, CHECK_BYTES);
        let c = parse_check(&b[..n]).unwrap();
        assert_eq!(
            (c.tick, c.wh, c.ct_ms),
            (1 << 33, 0xFEED_FACE_1234_5678, 999)
        );

        let v = Verdict {
            tick: 42,
            now: 43,
            ct_ms: 44,
            flags: VERDICT_KNOWN | VERDICT_OK,
            cw: [0xAA, 0xBB, 0xCC, 0xDD],
            pw: [0x00, 0x11, 0x22, 0x33],
        };
        let n = encode_verdict(&mut b, &v);
        assert_eq!(n, VERDICT_BYTES);
        let g = parse_verdict(&b[..n]).unwrap();
        assert_eq!(
            (g.tick, g.now, g.ct_ms, g.flags, g.cw, g.pw),
            (v.tick, v.now, v.ct_ms, v.flags, v.cw, v.pw)
        );
        // length is the whole contract for an unframed datagram
        assert!(parse_verdict(&b[..n - 1]).is_none());
        assert!(parse_check(&b[..n]).is_none());
    }

    // The bytes the Go authority pins (scratchpad/proto4-goldens.txt); the
    // TS host holds the same table. Three independent encoders agreeing on
    // these is the whole cross-language contract.
    const G_HELLO: &str = "18010400ffffffffffffffff00000000000000000300746b74";
    const G_INTENT: &str = "1702080706050403020104000a0088776655443322110700";
    const G_RESYNC: &str = "09039210000000000000";
    const G_WELCOME: &str = concat!(
        "2e1001000000070000000000000000040000000000001800000001",
        "efcdab89674523010b7061726b2d6d7974687261"
    );
    const G_EVENT: &str = concat!(
        "2511090000000000000000040000000000000300050000000000000008",
        "008877665544332211"
    );
    const G_REJECT: &str = "0d12050000000000000065000000";
    const G_SNAPSHOT: &str = concat!(
        "2d130900000000000000000400000000000001000000efbefecacefaedfe",
        "efcdab896745230104000000deadbeef"
    );
    const G_CHECK: &str = "010004000000000000efbefecacefaedfe0068e5cf8b010000";
    const G_VERDICT: &str = "02000400000000000006040000000000000068e5cf8b010000039abcdef012345678";

    fn hex(b: &[u8]) -> String {
        b.iter().map(|x| format!("{x:02x}")).collect()
    }

    fn unhex(s: &str) -> Vec<u8> {
        (0..s.len() / 2)
            .map(|i| u8::from_str_radix(&s[2 * i..2 * i + 2], 16).unwrap())
            .collect()
    }

    // Every encoder, against the pinned bytes: the frame's declared length
    // must equal the body actually written (the assertion that caught a
    // four-byte encoder bug on the Go side), and the decode must return
    // the values the vector documents.
    fn golden_frame(golden: &str, kind: u8, n: usize, buf: &[u8]) -> Vec<u8> {
        assert_eq!(hex(&buf[..n]), golden, "encoded bytes");
        let bytes = unhex(golden);
        assert_eq!(n, bytes.len());
        let (k, off, len, total) = frame_bounds(&bytes).unwrap().unwrap();
        assert_eq!(k, kind);
        assert_eq!(total, bytes.len(), "declared length vs body written");
        assert_eq!(off + len, total, "payload must fill the declared body");
        bytes
    }

    #[test]
    fn client_frames_match_the_pinned_goldens() {
        let mut b = [0u8; 256];

        let n = encode_hello(&mut b, -1, 0, b"tkt");
        let bytes = golden_frame(G_HELLO, K_HELLO, n, &b);
        let (_, off, len, _) = frame_bounds(&bytes).unwrap().unwrap();
        let h = parse_hello(&bytes[off..off + len]).unwrap();
        assert_eq!(
            (h.proto, h.since_seq, h.since_tick, h.ticket),
            (4, -1, 0, &b"tkt"[..])
        );

        let mut p = [0u8; 10];
        p[..8].copy_from_slice(&0x1122_3344_5566_7788u64.to_le_bytes());
        p[8..].copy_from_slice(&7u16.to_le_bytes());
        let n = encode_intent(&mut b, 0x0102_0304_0506_0708, 4, &p);
        let bytes = golden_frame(G_INTENT, K_INTENT, n, &b);
        let (_, off, len, _) = frame_bounds(&bytes).unwrap().unwrap();
        let i = parse_intent(&bytes[off..off + len]).unwrap();
        assert_eq!((i.id, i.kind, i.p), (0x0102_0304_0506_0708, 4, &p[..]));

        let n = encode_resync(&mut b, 4242);
        let bytes = golden_frame(G_RESYNC, K_RESYNC, n, &b);
        let (_, off, len, _) = frame_bounds(&bytes).unwrap().unwrap();
        assert_eq!(parse_resync(&bytes[off..off + len]), Some(4242));
    }

    #[test]
    fn server_frames_match_the_pinned_goldens() {
        let mut b = [0u8; 256];

        let w = Welcome {
            epoch: 1,
            seq: 7,
            tick: 1024,
            hz: 24,
            role: 1,
            terrain: 0x0123_4567_89AB_CDEF,
            park: b"park-mythra",
        };
        let n = encode_welcome(&mut b, &w);
        let bytes = golden_frame(G_WELCOME, K_WELCOME, n, &b);
        let (_, off, len, _) = frame_bounds(&bytes).unwrap().unwrap();
        let g = parse_welcome(&bytes[off..off + len]).unwrap();
        assert_eq!(
            (g.epoch, g.seq, g.tick, g.hz, g.role, g.terrain, g.park),
            (
                1,
                7,
                1024,
                24,
                1,
                0x0123_4567_89AB_CDEF,
                &b"park-mythra"[..]
            )
        );

        let dog = 0x1122_3344_5566_7788u64.to_le_bytes();
        let e = Event {
            seq: 9,
            tick: 1024,
            kind: 3,
            intent: 5,
            p: &dog,
        };
        let n = encode_event(&mut b, &e);
        let bytes = golden_frame(G_EVENT, K_EVENT, n, &b);
        let (_, off, len, _) = frame_bounds(&bytes).unwrap().unwrap();
        let g = parse_event(&bytes[off..off + len]).unwrap();
        assert_eq!(
            (g.seq, g.tick, g.kind, g.intent, g.p),
            (9, 1024, 3, 5, &dog[..])
        );

        let n = encode_reject(&mut b, 5, 101);
        let bytes = golden_frame(G_REJECT, K_REJECT, n, &b);
        let (_, off, len, _) = frame_bounds(&bytes).unwrap().unwrap();
        let g = parse_reject(&bytes[off..off + len]).unwrap();
        assert_eq!((g.intent, g.reason), (5, 101));

        let s = Snapshot {
            seq: 9,
            tick: 1024,
            epoch: 1,
            wh: 0xFEED_FACE_CAFE_BEEF,
            terrain: 0x0123_4567_89AB_CDEF,
            z: &[0xDE, 0xAD, 0xBE, 0xEF],
        };
        let n = encode_snapshot(&mut b, &s);
        let bytes = golden_frame(G_SNAPSHOT, K_SNAPSHOT, n, &b);
        let (_, off, len, _) = frame_bounds(&bytes).unwrap().unwrap();
        let g = parse_snapshot(&bytes[off..off + len]).unwrap();
        assert_eq!(
            (g.seq, g.tick, g.epoch, g.wh, g.terrain, g.z),
            (s.seq, s.tick, s.epoch, s.wh, s.terrain, s.z)
        );
    }

    #[test]
    fn a_park_name_longer_than_the_wire_allows_is_clamped() {
        // The length rides in a u8: wrapping it would write a frame whose
        // declared name length disagrees with the bytes after it.
        let long = [b'x'; 300];
        let mut b = [0u8; 512];
        let n = encode_welcome(
            &mut b,
            &Welcome {
                epoch: 1,
                seq: 1,
                tick: 1,
                hz: 24,
                role: 0,
                terrain: 0,
                park: &long,
            },
        );
        let (k, off, len, total) = frame_bounds(&b[..n]).unwrap().unwrap();
        assert_eq!((k, total), (K_WELCOME, n));
        let w = parse_welcome(&b[off..off + len]).unwrap();
        assert_eq!(w.park.len(), 255);
        assert_eq!(w.park, &long[..255]);
    }

    #[test]
    fn datagrams_match_the_pinned_goldens() {
        let mut b = [0u8; 64];
        let n = encode_check(&mut b, 1024, 0xFEED_FACE_CAFE_BEEF, 1_700_000_000_000);
        assert_eq!(hex(&b[..n]), G_CHECK);
        let c = parse_check(&unhex(G_CHECK)).unwrap();
        assert_eq!(
            (c.tick, c.wh, c.ct_ms),
            (1024, 0xFEED_FACE_CAFE_BEEF, 1_700_000_000_000)
        );

        // cw/pw ride verbatim: hexing them left to right is the display
        // string, and the ABI's u32 is the little-endian load of the same
        // four bytes — formatting that u32 as hex would reverse it.
        let v = Verdict {
            tick: 1024,
            now: 1030,
            ct_ms: 1_700_000_000_000,
            flags: VERDICT_KNOWN | VERDICT_OK,
            cw: [0x9A, 0xBC, 0xDE, 0xF0],
            pw: [0x12, 0x34, 0x56, 0x78],
        };
        let n = encode_verdict(&mut b, &v);
        assert_eq!(hex(&b[..n]), G_VERDICT);
        let g = parse_verdict(&unhex(G_VERDICT)).unwrap();
        assert_eq!((g.cw, g.pw), (v.cw, v.pw));
        assert_eq!(hex(&g.pw), "12345678");
        assert_eq!(module_word(g.pw), 0x7856_3412);
        assert_eq!(module_word(g.pw).to_le_bytes(), g.pw);
    }

    #[test]
    fn a_body_longer_than_its_fields_declare_is_malformed() {
        // The decode half of "declared length equals bytes written": a
        // tolerated trailing byte would let an encoder bug on any of the
        // three sides drift silently instead of failing at the first frame.
        let mut b = [0u8; 256];
        let n = encode_reject(&mut b, 5, 101);
        let (_, off, len, _) = frame_bounds(&b[..n]).unwrap().unwrap();
        assert!(parse_reject(&b[off..off + len]).is_some());
        let mut fat = b[..n].to_vec();
        fat[0] += 1; // grow the declared body by one
        fat.push(0xFF);
        let (_, off, len, _) = frame_bounds(&fat).unwrap().unwrap();
        assert!(parse_reject(&fat[off..off + len]).is_none());

        let n = encode_event(
            &mut b,
            &Event {
                seq: 9,
                tick: 1024,
                kind: 3,
                intent: 5,
                p: &[1u8; 8],
            },
        );
        let mut fat = b[..n].to_vec();
        fat[0] += 1;
        fat.push(0xFF);
        let (_, off, len, _) = frame_bounds(&fat).unwrap().unwrap();
        assert!(parse_event(&fat[off..off + len]).is_none());
    }

    #[test]
    fn rfc_9000_a1_non_minimal_varints_decode() {
        // The RFC's own example: 4025 is a legal two-byte encoding of 37.
        assert_eq!(get_varint(&[0x40, 0x25]), Some((37, 2)));
        assert_eq!(get_varint(&[0x25]), Some((37, 1)));
        let mut b = [0u8; 8];
        assert_eq!(put_varint(&mut b, 37), 1, "the encoder emits shortest form");
        assert_eq!(b[0], 0x25);
    }

    #[test]
    fn a_frame_is_invisible_until_its_last_byte_arrives() {
        let mut b = [0u8; 512];
        let n = encode_event(
            &mut b,
            &Event {
                seq: 1,
                tick: 2,
                kind: 3,
                intent: 4,
                p: &[7u8; 200],
            },
        );
        for cut in 0..n {
            assert_eq!(frame_bounds(&b[..cut]), Ok(None), "cut at {cut}");
        }
        assert!(frame_bounds(&b[..n]).unwrap().is_some());
        // a zero-length body can carry no kind byte: unreadable stream
        assert_eq!(frame_bounds(&[0x00]), Err(()));
    }
}

// ---- invariant 2: seq-dense application ----

#[test]
fn events_apply_in_seq_order_at_their_tick() {
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.run_to_tick(1030);
    r.m.clear();
    let t = r.s.tick();
    // scrambled on the wire; seq order is the law
    r.event(103, t, 3, 0, &DOG.to_le_bytes());
    r.event(101, t, 1, 0, &DOG.to_le_bytes());
    r.event(102, t, 3, 0, &DOG.to_le_bytes());
    r.pump();
    assert_eq!(
        r.m.emits_of(T_EVENT_APPLIED),
        vec![(101, t), (102, t), (103, t)]
    );
    assert_eq!(r.s.seq(), 103);
    assert_eq!(r.m.applies(), vec![1, 3, 3]);
}

#[test]
fn a_seq_gap_waits_for_the_missing_event() {
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.run_to_tick(1030);
    r.m.clear();
    let t = r.s.tick();
    r.event(102, t, 3, 0, &DOG.to_le_bytes());
    r.pump();
    r.pump();
    assert!(
        r.m.emits_of(T_EVENT_APPLIED).is_empty(),
        "applied over a gap"
    );
    assert_eq!(r.s.seq(), 100);
    r.event(101, t, 1, 0, &DOG.to_le_bytes());
    r.pump();
    assert_eq!(r.s.seq(), 102);
}

#[test]
fn a_future_event_waits_for_its_tick() {
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.run_to_tick(1020);
    r.m.clear();
    let target = r.s.tick() + 20;
    r.event(101, target, 1, 0, &DOG.to_le_bytes());
    r.pump();
    assert!(r.m.emits_of(T_EVENT_APPLIED).is_empty());
    r.run_to_tick(target);
    r.pump();
    assert_eq!(r.m.emits_of(T_EVENT_APPLIED), vec![(101, target)]);
}

#[test]
fn a_late_event_rolls_back_through_the_ring_and_replays() {
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    // stepping past 1032 and 1056 leaves those two snapshot ring entries
    r.run_to_tick(1040);
    let early = r.s.tick();
    r.event(101, early, 1, 0, &DOG.to_le_bytes());
    r.pump();
    assert_eq!(r.s.seq(), 101);
    r.run_to_tick(1060);
    let was = r.s.tick();
    r.m.clear();
    let late = early + 2;
    r.event(102, late, 3, 0, &DOG.to_le_bytes());
    r.pump();
    assert_eq!(r.m.count(Verb::Restore), 1, "one rollback");
    // (late_ticks, rewound_ticks): how late the event was, and how far
    // back the repair reached to place it. This one is later than the
    // dense window, so the rewind falls back to the cadence entry.
    assert_eq!(r.m.emits_of(T_ROLLBACK), vec![(was - late, was - 1032)]);
    assert_eq!(r.s.stat(STAT_ROLLBACKS), 1);
    assert_eq!(r.s.seq(), 102);
    // the event since the ring entry was replayed, then the late one landed
    assert_eq!(r.m.applies(), vec![1, 3]);
    // ...and the pump ends where it began: the rewind never reaches a frame
    assert_eq!(
        r.s.tick(),
        was,
        "the pump left the replica in the past: the renderer would rewind"
    );
    assert_eq!(r.s.stat(STAT_RESYNCS), 0);
}

#[test]
fn a_late_event_converges_on_the_same_world_as_an_early_one() {
    // Delivery order is not allowed to change the world. Same journal,
    // same ticks, one delivered late: the states must be bit-identical,
    // which is what makes a rollback invisible to every dog but the actor.
    fn journal(late: bool) -> (u64, u64) {
        let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
        r.run_to_tick(1040);
        let a = r.s.tick();
        r.event(101, a, 1, 0, &DOG.to_le_bytes());
        r.pump();
        if late {
            r.run_to_tick(1060);
            r.event(102, a + 2, 3, 0, &DOG.to_le_bytes());
            r.pump();
        } else {
            r.run_to_tick(a + 2);
            r.event(102, a + 2, 3, 0, &DOG.to_le_bytes());
            r.pump();
            r.run_to_tick(1060);
        }
        r.run_to_tick(1100);
        (r.s.tick(), r.m.park.acc)
    }
    let ordered = journal(false);
    let late = journal(true);
    assert_eq!(late, ordered, "a late event forked the world");
}

#[test]
fn a_rewind_too_deep_for_the_frame_resyncs_instead_of_half_rewinding() {
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.run_to_tick(1040);
    let early = r.s.tick();
    r.event(101, early, 1, 0, &DOG.to_le_bytes());
    r.pump();
    r.run_to_tick(1060);
    let was = r.s.tick();
    r.m.clear();
    r.event(102, early + 2, 3, 0, &DOG.to_le_bytes());
    // a budget that cannot pay for the walk back: refuse the whole repair
    r.s.pump(&mut r.m, r.now, 30);
    assert_eq!(r.s.tick(), was, "the replica rewound on a budget it lacked");
    assert_eq!(r.m.count(Verb::Restore), 0, "restored anyway");
    assert_eq!(r.s.stat(STAT_ROLLBACKS), 0);
    assert_eq!(
        r.m.emits_of(T_RESYNC_REQUESTED),
        vec![(R_LATE_EVENT as u64, 101)]
    );
}

#[test]
fn the_same_rewind_goes_through_on_a_real_frame_budget() {
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.run_to_tick(1040);
    let early = r.s.tick();
    r.event(101, early, 1, 0, &DOG.to_le_bytes());
    r.pump();
    r.run_to_tick(1060);
    let was = r.s.tick();
    r.event(102, early + 2, 3, 0, &DOG.to_le_bytes());
    r.pump();
    assert_eq!((r.s.tick(), r.s.seq()), (was, 102));
    assert_eq!(r.s.stat(STAT_ROLLBACKS), 1);
    assert_eq!(r.s.stat(STAT_RESYNCS), 0);
}

#[test]
fn a_restored_world_can_absorb_a_late_event_immediately() {
    // Cadence ring entries only appear on the second. Without a floor at
    // the restore itself, every late event in that first second costs a
    // snapshot — which empties the ring again. The loop repairs nothing
    // and the user watches the world teleport once per round trip.
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.m.clear();
    r.snapshot(500, 5000, 7, TERRAIN);
    r.pump();
    assert_eq!(r.s.tick(), 5000);
    assert_eq!(r.s.ring_len, 1, "the restore left no rollback floor");
    // a handful of ticks later, still short of the first cadence entry
    r.advance(200);
    let was = r.s.tick();
    assert!(
        was < 5000 + HZ,
        "test drifted past the first cadence snapshot"
    );
    r.m.clear();
    r.event(501, was - 2, 1, 0, &DOG.to_le_bytes());
    r.pump();
    assert_eq!(r.s.stat(STAT_ROLLBACKS), 1, "no floor to roll back to");
    assert_eq!(
        r.s.stat(STAT_RESYNCS),
        0,
        "answered a snapshot by asking for another"
    );
    assert_eq!(r.s.seq(), 501);
    assert_eq!(r.s.tick(), was);
}

#[test]
fn an_event_older_than_the_ring_resyncs_instead() {
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.run_to_tick(1060);
    r.m.clear();
    r.event(101, 900, 3, 0, &DOG.to_le_bytes());
    r.pump();
    assert_eq!(
        r.m.emits_of(T_RESYNC_REQUESTED),
        vec![(R_LATE_EVENT as u64, 100)]
    );
    assert_eq!(r.m.frame_kinds(), vec![wire::K_RESYNC]);
    assert_eq!(r.s.stat(STAT_RESYNCS), 1);
}

// ---- invariant 3: the ring entry is the state at entry to the tick ----

#[test]
fn the_ring_entry_is_the_state_before_the_ticks_own_events() {
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.run_to_tick(1030);
    let t = r.s.tick();
    let entry = r.s.hash_get(t).expect("hash at the current tick");
    assert_eq!(entry, toy_hash(t, r.m.park.acc));
    // apply an event stamped for this very tick, without leaving it
    r.event(101, t, 1, 0, &DOG.to_le_bytes());
    r.pump();
    assert_eq!(r.s.tick(), t, "wall time did not move");
    assert_eq!(r.s.seq(), 101);
    assert_ne!(
        toy_hash(t, r.m.park.acc),
        entry,
        "the event must have moved the world"
    );
    assert_eq!(
        r.s.hash_get(t),
        Some(entry),
        "re-hashing after this tick's events would poison the entry a check samples"
    );
}

#[test]
fn checks_carry_the_ring_hash_for_the_current_tick() {
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.advance(3000);
    let checks = r.m.checks();
    assert!(!checks.is_empty(), "an idle replica still checks");
    for (tick, wh) in &checks {
        assert_eq!(Some(*wh), r.s.hash_get(*tick), "check at tick {tick}");
    }
    assert_eq!(r.s.stat(STAT_CHECKS), checks.len() as u64);
}

#[test]
fn a_hidden_replica_goes_quiet() {
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.s.set_visible(false);
    r.m.clear();
    r.advance(20_000);
    assert!(r.m.dgs.is_empty(), "hidden replicas send no checks");
    r.s.set_visible(true);
    r.advance(100);
    assert!(!r.m.dgs.is_empty(), "and resume when shown");
}

// ---- invariant 9: an idle session costs checks and nothing else ----

#[test]
fn an_idle_session_sends_only_checks() {
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.m.clear();
    r.advance(60_000);
    assert!(
        r.m.sent.is_empty(),
        "idle stream traffic: {:?}",
        r.m.frame_kinds()
    );
    assert!(
        (11..=13).contains(&r.m.dgs.len()),
        "one check per five seconds, got {}",
        r.m.dgs.len()
    );
    for d in &r.m.dgs {
        assert_eq!(d.len(), wire::CHECK_BYTES);
    }
}

// ---- invariant 4: two strikes, one resync ----

#[test]
fn two_strikes_make_one_resync_and_an_ok_clears_them() {
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.advance(1000);
    r.m.clear();
    r.verdict(r.s.tick(), true, false, 0);
    assert!(
        r.m.emits_of(T_RESYNC_REQUESTED).is_empty(),
        "one strike is not enough"
    );
    r.verdict(r.s.tick(), true, true, 0); // an ok resets the count
    r.verdict(r.s.tick(), true, false, 0);
    assert!(r.m.emits_of(T_RESYNC_REQUESTED).is_empty());
    r.verdict(r.s.tick(), true, false, 0);
    assert_eq!(r.m.emits_of(T_RESYNC_REQUESTED).len(), 1);
    assert_eq!(r.m.frame_kinds(), vec![wire::K_RESYNC]);
    assert_eq!(r.s.stat(STAT_MISMATCHES), 3);
    assert_eq!(r.s.stat(STAT_RESYNCS), 1);
    // the latch holds: further strikes do not pile on resyncs
    r.verdict(r.s.tick(), true, false, 0);
    r.verdict(r.s.tick(), true, false, 0);
    assert_eq!(r.s.stat(STAT_RESYNCS), 1);
}

#[test]
fn an_aged_out_check_strikes_too() {
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.advance(1000);
    r.m.clear();
    r.verdict(r.s.tick(), false, false, 0);
    r.verdict(r.s.tick(), false, false, 0);
    assert_eq!(
        r.m.emits_of(T_RESYNC_REQUESTED),
        vec![(R_CHECK_AGED_OUT as u64, 100)]
    );
    assert_eq!(r.s.stat(STAT_MISMATCHES), 0, "unknown is not a mismatch");
}

// ---- invariant 5 and 6: snapshots, terrain, and the wrong world ----

#[test]
fn the_welcome_telemetry_carries_the_granted_role() {
    // Hosts read the role the authority actually granted from this emit;
    // it is the reason none of them needs a second wire decoder.
    for role in [ROLE_SPECTATOR, ROLE_PLAYER] {
        let mut r = Rig::new(role);
        r.s.connected(&mut r.m, b"ticket", 0);
        r.m.clear();
        r.welcome(2000, TERRAIN);
        assert_eq!(
            r.m.emits_of(T_WELCOME),
            vec![(3, HZ | ((role as u64) << 32))],
            "role {role}"
        );
        assert_eq!(r.s.role, role);
    }
}

#[test]
fn a_snapshot_waits_for_its_terrain_then_lands() {
    let mut r = Rig::new(ROLE_PLAYER);
    r.s.connected(&mut r.m, b"ticket", 0);
    r.welcome(2000, TERRAIN);
    assert_eq!(r.m.reqs_of(REQ_NEED_TERRAIN), vec![TERRAIN]);
    r.m.clear();
    r.snapshot(500, 2000, 9, TERRAIN);
    assert_eq!(r.m.count(Verb::Restore), 0, "restored blind");
    assert_eq!(r.m.reqs_of(REQ_NEED_TERRAIN), vec![TERRAIN]);
    assert!(!r.s.have_state);
    r.s.terrain_ready(&mut r.m, TERRAIN, true);
    r.pump();
    assert_eq!(r.m.count(Verb::Restore), 1);
    assert!(r.s.have_state);
    assert_eq!(r.s.seq(), 500);
    assert_eq!(r.s.tick(), 2000);
    assert_eq!(r.m.emits_of(T_SNAPSHOT_RESTORED), vec![(500, 2000)]);
    assert!(
        r.m.emits_of(T_MISMATCH).is_empty(),
        "the restore matched wh"
    );
}

#[test]
fn a_restore_clears_the_queues_and_resends_unanswered_intents() {
    let mut r = Rig::boot(ROLE_PLAYER, 1008);
    r.run_to_tick(1040);
    // an intent the journal has not answered, and a future event queued
    let join_id = r.m.intents()[0].0;
    let id = r.s.intent_check_in(&mut r.m, r.now);
    r.event(400, 5000, 1, 0, &DOG.to_le_bytes());
    r.m.clear();
    r.snapshot(300, 4000, 11, TERRAIN);
    r.pump();
    assert_eq!(r.s.seq(), 300);
    assert_eq!(r.s.tick(), 4000);
    assert_eq!(r.s.queued_len, 0, "the queue does not survive a restore");
    assert_eq!(r.s.recent_len, 0);
    // the restored state itself is the only ring entry: nothing older
    // survives, but the world is never left without a rollback floor
    assert_eq!(r.s.ring_len, 1);
    // nothing rides again: this connection already carried both
    assert!(
        r.m.intents().is_empty(),
        "a restore resent intents the connection had already delivered"
    );
    // ...but a fresh connection carries them, exactly once each
    r.s.disconnected();
    r.m.clear();
    r.s.connected(&mut r.m, b"ticket", r.now);
    let sent = r.m.intents();
    assert_eq!(sent.len(), 1, "the join rides the new connection");
    assert!(sent.contains(&(join_id, EV_JOIN)));
    let _ = id;
    assert_eq!(clock_state_code(r.s.clock.state()), 1, "clock re-seeded");
}

#[test]
fn a_snapshot_that_disagrees_with_its_own_hash_is_surfaced() {
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.m.clear();
    let mut buf = [0u8; 128];
    let n = wire::encode_snapshot(
        &mut buf,
        &wire::Snapshot {
            seq: 200,
            tick: 3000,
            epoch: 3,
            wh: 0xDEAD,
            terrain: TERRAIN,
            z: &toy_state(3000, 5),
        },
    );
    r.feed(&buf[..n].to_vec());
    r.pump();
    assert!(r.s.have_state);
    assert_eq!(r.m.emits_of(T_MISMATCH).len(), 1);
    assert_eq!(r.s.stat(STAT_MISMATCHES), 1);
}

#[test]
fn a_snapshot_for_the_wrong_world_is_surfaced_not_looped() {
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.m.clear();
    r.m.restore_code = RESTORE_WRONG_TERRAIN;
    r.snapshot(300, 3000, 5, TERRAIN);
    r.pump();
    assert_eq!(r.m.count(Verb::Restore), 1, "tried exactly once");
    assert_eq!(
        r.m.emits_of(T_RESTORE_FAILED),
        vec![(RESTORE_WRONG_TERRAIN as u64, 300)]
    );
    r.advance(30_000);
    assert_eq!(
        r.m.count(Verb::Restore),
        1,
        "a terrain mismatch must not become a resync loop"
    );
    assert!(r.m.emits_of(T_RESYNC_REQUESTED).is_empty());
}

#[test]
fn a_failed_terrain_fetch_retries_the_resync() {
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.m.clear();
    r.snapshot(300, 3000, 5, 0x9999);
    assert_eq!(r.m.reqs_of(REQ_NEED_TERRAIN), vec![0x9999]);
    r.s.terrain_ready(&mut r.m, 0x9999, false);
    r.advance(1000);
    assert!(
        r.m.emits_of(T_RESYNC_REQUESTED).is_empty(),
        "retried too soon"
    );
    r.advance(3000);
    assert_eq!(
        r.m.emits_of(T_RESYNC_REQUESTED),
        vec![(R_TERRAIN_FETCH as u64, 100)]
    );
}

// ---- invariant 7: one module swap per epoch ----

#[test]
fn an_epoch_event_asks_for_the_module_once() {
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.run_to_tick(1030);
    r.m.clear();
    let mut p = [0u8; 12];
    p[..4].copy_from_slice(&4u32.to_le_bytes());
    p[4..12].copy_from_slice(&0x9999_8888_2222_2222u64.to_le_bytes());
    r.event(101, r.s.tick(), EV_EPOCH_ADVANCE, 0, &p);
    r.pump();
    assert_eq!(r.m.reqs_of(REQ_NEED_MODULE), vec![PARK_B as u64]);
    assert_eq!(r.m.emits_of(T_MODULE_SWAP_WANTED), vec![(PARK_B as u64, 0)]);
    assert_eq!(
        r.m.emits_of(T_RESYNC_REQUESTED),
        vec![(R_MODULE_EPOCH as u64, 101)]
    );
    // a second epoch event before the swap lands must not re-ask
    r.event(102, r.s.tick(), EV_EPOCH_ADVANCE, 0, &p);
    r.pump();
    assert_eq!(r.m.reqs_of(REQ_NEED_MODULE).len(), 1, "the latch held");
}

#[test]
fn a_verdict_naming_another_module_is_the_backstop() {
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.advance(1000);
    r.m.clear();
    r.verdict(r.s.tick(), true, true, PARK_B);
    assert_eq!(r.m.reqs_of(REQ_NEED_MODULE), vec![PARK_B as u64]);
    r.verdict(r.s.tick(), true, true, PARK_B);
    assert_eq!(r.m.reqs_of(REQ_NEED_MODULE).len(), 1, "asked once");
    // the host swaps both slots: the fresh instances hold no world, so the
    // core drops its state and takes a snapshot into them
    r.m.clear();
    r.s.module_swapped(&mut r.m, PARK_B);
    assert!(!r.s.have_state);
    r.pump();
    assert_eq!(
        r.m.emits_of(T_RESYNC_REQUESTED),
        vec![(R_MODULE_SWAPPED as u64, 100)]
    );
    assert_eq!(r.m.count(Verb::Step), 0, "no stepping without a world");
    r.snapshot(700, 9000, 3, TERRAIN);
    r.pump();
    assert!(r.s.have_state);
    assert_eq!(r.s.tick(), 9000);
}

// ---- invariant 8: an event is ours only under our own intent id ----

#[test]
fn only_an_exact_intent_id_acknowledges_a_pending_intent() {
    // The wire dropped the actor, so the intent id is the only handle back
    // to a sender. A bare counter would collide across senders and page
    // loads; these two ids share a counter and differ in nonce, and the
    // foreign one must not touch our pending set.
    let mut r = Rig::boot(ROLE_PLAYER, 1008);
    r.confirm_join();
    r.m.clear();
    let mine = r.s.intent_move_to(&mut r.m, 42, r.now);
    assert_eq!(mine >> 32, NONCE as u64);
    let theirs = ((NONCE as u64 ^ 0xFFFF) << 32) | (mine & 0xFFFF_FFFF);
    assert_ne!(mine, theirs);
    let before = r.s.intents_len;
    let next = r.s.seq() + 1;
    r.event(next, r.s.tick(), EV_MOVE_TO, theirs, &move_payload(9));
    r.pump();
    assert_eq!(
        r.s.intents_len, before,
        "a foreign event cleared our intent"
    );
    // it lands on the world, as every journal event does — what it must
    // not do is retire our pending entry
    assert_eq!(r.m.applies(), vec![EV_MOVE_TO]);
    // and the real acknowledgment still lands
    r.m.clear();
    let next = r.s.seq() + 1;
    r.event(next, r.s.tick(), EV_MOVE_TO, mine, &move_payload(42));
    r.pump();
    assert_eq!(r.s.intents_len, before - 1);
}

#[test]
fn signing_in_swaps_identity_without_reloading_the_world() {
    const NEW_DOG: u64 = 0x5151_5151_5151_5151;
    const NEW_NONCE: u32 = 0x0BAD_F00D;
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.run_to_tick(1030);
    let (seq, tick) = (r.s.seq(), r.s.tick());
    r.s.intent_check_in(&mut r.m, r.now);
    assert_eq!(r.s.intents_len, 1);

    r.s.reidentify(NEW_DOG, ROLE_PLAYER, NEW_NONCE, r.now);
    assert_eq!(
        r.s.intents_len, 0,
        "pending intents belonged to the old dog"
    );
    assert_eq!((r.s.seq(), r.s.tick()), (seq, tick), "the replica survives");

    // the host redials: the upgrade is a reconnect, not a reload
    r.s.disconnected();
    r.m.clear();
    r.s.connected(&mut r.m, b"upgraded-ticket", r.now);
    let f = &r.m.sent[0];
    let (kind, off, len, _) = wire::frame_bounds(f).unwrap().unwrap();
    assert_eq!(kind, wire::K_HELLO);
    let h = wire::parse_hello(&f[off..off + len]).unwrap();
    assert_eq!((h.since_seq, h.since_tick), (seq, tick));

    // and the new dog joins under the new id space
    let f = &r.m.sent[1];
    let (kind, off, len, _) = wire::frame_bounds(f).unwrap().unwrap();
    assert_eq!(kind, wire::K_INTENT);
    let i = wire::parse_intent(&f[off..off + len]).unwrap();
    assert_eq!(i.kind, EV_JOIN);
    assert_eq!(i.id >> 32, NEW_NONCE as u64);
    assert_eq!(
        i.id & 0xFFFF_FFFF,
        1,
        "the counter restarts under a fresh nonce"
    );
    assert_eq!(i.p, &NEW_DOG.to_le_bytes()[..]);
}

#[test]
fn an_event_the_replica_refuses_is_a_resync() {
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.run_to_tick(1030);
    r.m.reject = Some((EV_CHECK_IN, 5));
    r.m.clear();
    r.event(101, r.s.tick(), EV_CHECK_IN, 0, &DOG.to_le_bytes());
    r.pump();
    assert_eq!(
        r.m.emits_of(T_RESYNC_REQUESTED),
        vec![(R_EVENT_REJECTED as u64, 100)]
    );
    assert_eq!(r.s.seq(), 100, "a refused event never advances seq");
}

// ---- invariant 10: nothing moves the world without saying so ----

#[test]
fn every_path_that_corrects_the_world_announces_it() {
    // The four are enumerated on S_CORRECTED itself. A path that moves
    // rendered positions without raising the bit is a jump on screen with
    // nothing to absorb it, and the presenter cannot tell that it missed
    // one — so each is held here rather than trusted.
    let mut r = Rig::boot(ROLE_SPECTATOR, 3000);
    // (1) a snapshot restore, landing mid-frame the way one really does:
    // the flag has to survive from the restore to the next pump
    r.snapshot(500, 3200, 11, TERRAIN);
    assert_eq!(r.s.tick(), 3200, "the snapshot never landed");
    assert_ne!(
        r.pump() & S_CORRECTED,
        0,
        "(1) a snapshot restore left the host nothing to absorb"
    );

    // (2) a repair: an event late by two ticks rewinds and replays
    r.advance(200);
    let was = r.s.tick();
    r.event(r.s.seq() + 1, was - 2, EV_CHECK_IN, 0, &DOG.to_le_bytes());
    let flags = r.pump();
    assert_eq!(r.s.stat(STAT_ROLLBACKS), 1, "the repair never happened");
    assert_ne!(
        flags & S_CORRECTED,
        0,
        "(2) a repair moved the world quietly"
    );

    // (3) has its own case below; (4) a module swap drops the world, and
    // the restore that refills it is what the host hears about
    r.s.module_swapped(&mut r.m, PARK_B);
    assert!(!r.s.have_state, "a fresh instance holds no world");
    r.pump();
    r.snapshot(700, 9000, 3, TERRAIN);
    let flags = r.pump();
    assert!(r.s.have_state);
    assert_ne!(
        flags & S_CORRECTED,
        0,
        "(4) the world was replaced under the renderer in silence"
    );
}

#[test]
fn a_terrain_change_announces_itself_and_a_plain_edit_does_not() {
    // S_CORRECTED is the host's only warning that dogs moved with no time
    // behind it, so every mutation that is not the step function owes it —
    // and no other one may raise it, or the presenter glides away motion
    // the world meant.
    let mut r = Rig::boot(ROLE_PLAYER, 2000);
    r.confirm_join();

    let at = r.s.tick() + 3;
    r.event(r.s.seq() + 1, at, EV_MOVE_TO, 0, &move_payload(9));
    let flags = r.pump_until_seq(r.s.seq() + 1);
    assert_eq!(
        flags & S_CORRECTED,
        0,
        "an edit the step function acts on later moved nothing now"
    );

    let at = r.s.tick() + 3;
    let mut p = [0u8; 12];
    p[..4].copy_from_slice(&7u32.to_le_bytes());
    p[4..].copy_from_slice(&TERRAIN.to_le_bytes());
    r.event(r.s.seq() + 1, at, EV_TERRAIN_SET, 0, &p);
    let flags = r.pump_until_seq(r.s.seq() + 1);
    assert_ne!(
        flags & S_CORRECTED,
        0,
        "a terrain change puts dogs back on ground: the host is never told"
    );
    assert_eq!(
        (r.s.stat(STAT_ROLLBACKS), r.s.stat(STAT_RESYNCS)),
        (0, 0),
        "neither event needed a repair, so the bit came from the apply"
    );
}

#[test]
fn a_clock_skip_leaves_the_core_standing_where_the_park_does() {
    // The one event that moves the tick without a step. A core that
    // assumed its own tick would keep stepping from where it thought it
    // was, and every later event would be judged late or early against a
    // clock the park had already left.
    let mut r = Rig::boot(ROLE_SPECTATOR, 2000);
    let at = r.s.tick() + 3;
    let target = at + 5_000;
    r.event(r.s.seq() + 1, at, EV_CLOCK_SKIP, 0, &target.to_le_bytes());
    r.pump_until_seq(r.s.seq() + 1);
    assert_eq!(
        r.s.tick(),
        r.m.park.tick,
        "the core's tick is the park's, always"
    );
    assert!(r.s.tick() >= target, "the skip was not taken");
}

// ---- reject policy ----

#[test]
fn a_join_the_park_already_honored_is_not_an_error() {
    let mut r = Rig::boot(ROLE_PLAYER, 1008);
    let join = r.m.intents()[0].0;
    r.m.clear();
    r.reject(join, ERR_PRESENT);
    assert!(
        r.m.emits_of(T_REJECT).is_empty(),
        "present-on-join is the joined state"
    );
    assert_eq!(r.s.stat(STAT_REJECTS), 1);
    assert_eq!(r.s.intents_len, 0);
}

#[test]
fn a_boost_already_in_effect_is_not_an_error() {
    let mut r = Rig::boot(ROLE_PLAYER, 1008);
    r.confirm_join();
    r.m.clear();
    let id = r.s.intent_boost(&mut r.m, true, r.now);
    r.reject(id, ERR_NOOP);
    assert!(r.m.emits_of(T_REJECT).is_empty());
}

#[test]
fn absent_on_a_players_intent_rejoins_the_park_once_per_window() {
    let mut r = Rig::boot(ROLE_PLAYER, 1008);
    r.confirm_join();
    r.m.clear();
    let id = r.s.intent_check_in(&mut r.m, r.now);
    r.reject(id, ERR_ABSENT);
    let sent = r.m.intents();
    assert_eq!(
        sent.iter().map(|i| i.1).collect::<Vec<_>>(),
        vec![EV_CHECK_IN, EV_JOIN, EV_CHECK_IN],
        "re-join, then the intent behind it"
    );
    assert_eq!(r.m.emits_of(T_AUTO_REJOIN).len(), 1);
    // ids are fresh: a resend under an old id would be swallowed as a dupe
    assert_ne!(sent[0].0, sent[2].0);

    // a second absent inside the window does not re-join
    r.s.presence = true;
    r.m.clear();
    let id2 = r.s.intent_check_in(&mut r.m, r.now);
    r.reject(id2, ERR_ABSENT);
    assert!(r.m.emits_of(T_AUTO_REJOIN).is_empty());
    // past the window it does
    r.now += 6000;
    r.s.presence = true;
    r.m.clear();
    let id3 = r.s.intent_check_in(&mut r.m, r.now);
    r.reject(id3, ERR_ABSENT);
    assert_eq!(r.m.emits_of(T_AUTO_REJOIN).len(), 1);
}

// A snapshot frame at the largest size the park can produce: the park's
// io buffer is the ceiling on the state, so it is the ceiling on the
// deflate payload too.
fn big_snapshot_frame(seq: i64, tick: u64, zlen: usize) -> Vec<u8> {
    let z = vec![0x5A; zlen];
    let mut buf = vec![0u8; zlen + 64];
    let n = wire::encode_snapshot(
        &mut buf,
        &wire::Snapshot {
            seq,
            tick,
            epoch: 3,
            wh: 0,
            terrain: TERRAIN,
            z: &z,
        },
    );
    buf.truncate(n);
    buf
}

#[test]
fn a_frame_larger_than_a_host_chunk_survives_the_chunk_boundary() {
    // The host stages 64 KiB at a time, so a full-size snapshot frame is
    // always split — and while its tail is still pending, another whole
    // chunk can land on top. If reassembly cannot hold both, the decoder
    // realigns mid-frame on garbage and the session is finished.
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.m.clear();
    let mut stream = Vec::new();
    stream.extend_from_slice(&big_snapshot_frame(200, 5000, 64 * 1024));
    stream.extend_from_slice(&big_snapshot_frame(201, 5001, 64 * 1024));
    let mut tail = [0u8; 128];
    let n = wire::encode_snapshot(
        &mut tail,
        &wire::Snapshot {
            seq: 300,
            tick: 4000,
            epoch: 3,
            wh: toy_hash(4000, 9),
            terrain: TERRAIN,
            z: &toy_state(4000, 9),
        },
    );
    stream.extend_from_slice(&tail[..n]);

    for chunk in stream.chunks(64 * 1024) {
        r.s.on_stream(&mut r.m, chunk, r.now);
    }
    r.pump();
    assert!(
        r.m.reqs_of(REQ_TEARDOWN).is_empty(),
        "reassembly gave up inside a legal frame"
    );
    // the frame behind the big ones is the proof the decoder stayed aligned
    assert!(r.s.have_state);
    assert_eq!((r.s.seq(), r.s.tick()), (300, 4000));
}

#[test]
fn a_stream_too_big_to_reassemble_is_torn_down_not_resynced() {
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.m.clear();
    // a frame that promises far more than reassembly can ever hold
    let mut chunk = Vec::new();
    let mut v = [0u8; 8];
    let n = wire::put_varint(&mut v, 400_000);
    chunk.extend_from_slice(&v[..n]);
    chunk.push(wire::K_SNAPSHOT);
    chunk.resize(64 * 1024, 0);
    for _ in 0..3 {
        r.s.on_stream(&mut r.m, &chunk, r.now);
    }
    assert_eq!(r.m.reqs_of(REQ_TEARDOWN), vec![R_STREAM_OVERFLOW as u64]);
    assert!(
        r.m.emits_of(T_RESYNC_REQUESTED).is_empty(),
        "a resync travels the same broken stream and can never repair it"
    );
    assert_eq!(r.s.stat(STAT_RESYNCS), 0);
    assert_eq!(r.s.in_len, 0);
}

#[test]
fn an_unreadable_length_prefix_is_torn_down_not_resynced() {
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.m.clear();
    r.s.on_stream(&mut r.m, &[0x00], r.now); // a body that cannot hold a kind
    assert_eq!(r.m.reqs_of(REQ_TEARDOWN), vec![R_FRAMING as u64]);
    assert!(r.m.emits_of(T_RESYNC_REQUESTED).is_empty());
    assert_eq!(r.s.stat(STAT_RESYNCS), 0);
}

#[test]
fn a_failed_fetch_for_another_world_leaves_the_held_snapshot_alone() {
    const OTHER: u64 = 0xBEEF_0001;
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.m.clear();
    r.snapshot(400, 6000, 3, OTHER);
    assert_eq!(r.m.reqs_of(REQ_NEED_TERRAIN), vec![OTHER]);
    assert!(r.s.held_valid);
    // a fetch we stopped caring about fails
    r.s.terrain_ready(&mut r.m, 0x1234_5678, false);
    assert!(
        r.s.held_valid,
        "a superseded fetch discarded the snapshot we are holding"
    );
    // and the world we are actually waiting on still lands
    r.s.terrain_ready(&mut r.m, OTHER, true);
    r.pump();
    assert!(r.s.have_state);
    assert_eq!((r.s.seq(), r.s.tick()), (400, 6000));
}

#[test]
fn a_refused_world_stops_the_strike_loop_until_the_terrain_changes() {
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.advance(1000);
    r.m.restore_code = RESTORE_WRONG_TERRAIN;
    r.m.clear();
    r.snapshot(300, 3000, 5, TERRAIN);
    r.pump();
    assert_eq!(r.m.emits_of(T_RESTORE_FAILED).len(), 1);
    // divergence keeps being reported, but another snapshot would be
    // refused exactly the same way — asking again at the check cadence is
    // a loop, not a repair
    for _ in 0..10 {
        r.verdict(r.s.tick(), true, false, 0);
    }
    assert!(
        r.m.emits_of(T_RESYNC_REQUESTED).is_empty(),
        "resync loop against a world the park refuses"
    );
    assert_eq!(r.s.stat(STAT_RESYNCS), 0);
    // a different world is worth asking about again
    r.m.restore_code = 0;
    r.s.terrain_ready(&mut r.m, 0x9999, true);
    r.verdict(r.s.tick(), true, false, 0);
    r.verdict(r.s.tick(), true, false, 0);
    assert_eq!(r.m.emits_of(T_RESYNC_REQUESTED).len(), 1);
}

#[test]
fn a_zero_budget_observes_without_stepping() {
    // The dev-loop freeze: starve stepping and watch the clock notice.
    // Nothing about this is a special mode — it is what a replica whose
    // host never gets to run looks like, which is why it is worth being
    // able to produce on demand.
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.advance(500);
    let frozen_at = r.s.tick();
    assert!(frozen_at > 1008, "the replica should be stepping first");
    r.m.clear();

    // 40s starved: past the 30s hash ring, so the clock walks Locked ->
    // FastForward -> SnapshotRequired and asks for a snapshot on the way.
    let states = r.advance_starved(40_000);
    assert_eq!(r.s.tick(), frozen_at, "a zero budget must not step");
    assert_eq!(
        r.m.count(Verb::Step),
        0,
        "slot 0 stepped under a zero budget"
    );
    assert!(states.contains(&2), "never escalated to fast-forward");
    assert_eq!(
        *states.last().unwrap(),
        3,
        "never reached snapshot-required"
    );
    let asks = r.m.emits_of(T_RESYNC_REQUESTED);
    assert_eq!(
        asks[0],
        (R_CLOCK as u64, 100),
        "the first ask names the clock"
    );
    assert!(asks.len() > 1, "nobody answered, so it has to ask again");
    // A retry is the same request: same reason, same seq. One that
    // renumbered itself would be asking for a different snapshot than the
    // one it still needs, and one that renamed itself would show a
    // dashboard two causes where there was one.
    assert!(
        asks.iter().all(|a| *a == asks[0]),
        "a retry changed the request: {asks:?}"
    );
    // the clock kept observing, so it knows how far behind it is
    assert!(r.s.error_q16(r.now) >> 16 > 30 * HZ as i64);

    // ...and the world still moves when the budget comes back: the resync
    // it asked for lands, and stepping resumes from there.
    r.m.clear();
    r.snapshot(300, frozen_at + 1000, 5, TERRAIN);
    r.pump();
    assert_eq!(r.s.tick(), frozen_at + 1000);
    assert_eq!(clock_state_code(r.s.clock.state()), 1, "back to locked");
    r.advance(1000);
    assert!(r.s.tick() > frozen_at + 1000, "stepping never resumed");
}

#[test]
fn a_zero_budget_still_applies_what_the_journal_delivered() {
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.advance(500);
    let t = r.s.tick();
    r.m.clear();
    r.event(101, t, 1, 0, &DOG.to_le_bytes());
    r.s.pump(&mut r.m, r.now, 0);
    assert_eq!(r.s.seq(), 101, "a frozen replica still owes the journal");
    assert_eq!(r.m.applies(), vec![1]);
    assert_eq!(r.s.tick(), t, "applying is not stepping");
}

#[test]
fn the_clock_readouts_come_from_the_clock_being_disciplined() {
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.advance(1000);
    // An undisciplined clock never leaves Acquiring, never accrues a
    // fraction, and never learns an rtt: all three readouts would sit at
    // zero while pump reported Locked. The rtt is an EWMA seeded by the
    // welcome's same-ms echo, so it walks toward the real round trip
    // rather than jumping — which is itself the proof it is being fed.
    let start = r.s.rtt_ms();
    for _ in 0..10 {
        let mut buf = [0u8; 64];
        let n = wire::encode_verdict(
            &mut buf,
            &wire::Verdict {
                tick: r.s.tick(),
                now: r.s.tick(),
                ct_ms: r.now.saturating_sub(120),
                flags: wire::VERDICT_KNOWN | wire::VERDICT_OK,
                cw: [0; 4],
                pw: [0; 4],
            },
        );
        let dg = buf[..n].to_vec();
        r.s.on_datagram(&mut r.m, &dg, r.now);
        r.advance(100);
    }
    let rtt = r.s.rtt_ms();
    assert!(rtt > start, "rtt never moved off its seed ({start}ms)");
    assert!(
        (80..=120).contains(&rtt),
        "rtt {rtt} is not converging on 120"
    );

    let mut saw_phase = false;
    for _ in 0..125 {
        r.now += 16;
        let status = r.pump();
        saw_phase |= r.s.phase_q16() != 0;
        assert_eq!(
            (status >> S_CLOCK_SHIFT) & 3,
            clock_state_code(r.s.clock.state()),
            "pump status and the clock disagree"
        );
    }
    assert!(
        saw_phase,
        "phase never advanced: nothing is driving the clock"
    );
    assert_eq!(clock_state_code(r.s.clock.state()), 1);
    assert!(r.s.phase_q16() < 65536);
}

#[test]
fn a_redial_carries_the_same_join_rather_than_minting_another() {
    // The park is being asked to admit a dog it is already being asked to
    // admit. A second id is a second join on the wire, and the authority
    // can only answer the newcomer with a refusal.
    let mut r = Rig::boot(ROLE_PLAYER, 1008);
    let first = r.m.intents()[0].0;
    r.s.disconnected();
    r.m.clear();
    r.s.connected(&mut r.m, b"ticket", r.now);
    let sent = r.m.intents();
    assert_eq!(sent.len(), 1, "the redial put a second join on the wire");
    assert_eq!(sent[0], (first, EV_JOIN), "the join was minted afresh");
    assert_eq!(r.s.intents_len, 1);
}

#[test]
fn an_intent_that_needs_a_dog_waits_for_the_join_to_be_answered() {
    // Racing the join earns an ABSENT refusal, which triggers a re-join
    // and a retry — a spiral out of one tap on the boost button.
    let mut r = Rig::boot(ROLE_PLAYER, 1008);
    r.m.clear();
    let boost = r.s.intent_boost(&mut r.m, true, r.now);
    let node = r.s.intent_move_to(&mut r.m, 7, r.now);
    r.pump();
    assert!(
        r.m.intents().is_empty(),
        "presence-requiring intents raced the join"
    );
    assert_ne!(boost, 0, "a held intent still gets its id");
    // held means held: the wait is the round trip, and nothing is shown
    // of an action the park has not been told about
    r.pump();

    // the journal answers the join, and everything held goes at once
    r.confirm_join();
    let sent = r.m.intents();
    assert_eq!(
        sent,
        vec![(boost, EV_BOOST_SET), (node, EV_MOVE_TO)],
        "held intents did not flush in order, under their own ids"
    );
    assert!(r.s.presence);
}

#[test]
fn a_spectator_acts_without_waiting_for_a_dog() {
    // A spectator has no join to wait on; gating one would mean their
    // intents never leave at all.
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.m.clear();
    r.s.intent_check_in(&mut r.m, r.now);
    assert_eq!(r.m.intents().len(), 1);
}

#[test]
fn a_spectator_is_never_auto_rejoined() {
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    assert!(r.m.intents().is_empty(), "spectators do not bring a dog");
    r.m.clear();
    let id = r.s.intent_check_in(&mut r.m, r.now);
    r.reject(id, ERR_ABSENT);
    assert!(r.m.emits_of(T_AUTO_REJOIN).is_empty());
    assert_eq!(
        r.m.emits_of(T_REJECT),
        vec![(ERR_ABSENT as u64, EV_CHECK_IN as u64)]
    );
}

// ---- connection choreography ----

#[test]
fn the_hello_carries_what_the_replica_already_holds() {
    let mut r = Rig::boot(ROLE_PLAYER, 1008);
    r.run_to_tick(1030);
    let tick = r.s.tick();
    r.s.disconnected();
    r.m.clear();
    r.s.connected(&mut r.m, b"a-ticket", r.now);
    let f = &r.m.sent[0];
    let (kind, off, len, _) = wire::frame_bounds(f).unwrap().unwrap();
    assert_eq!(kind, wire::K_HELLO);
    let h = wire::parse_hello(&f[off..off + len]).unwrap();
    assert_eq!(h.proto, 4);
    assert_eq!((h.since_seq, h.since_tick), (100, tick));
    assert_eq!(h.ticket, b"a-ticket");
    assert_eq!(r.m.frame_kinds(), vec![wire::K_HELLO, wire::K_INTENT]);
}

#[test]
fn frames_split_across_reads_reassemble() {
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.run_to_tick(1030);
    r.m.clear();
    let mut buf = [0u8; 256];
    let n = wire::encode_event(
        &mut buf,
        &wire::Event {
            seq: 101,
            tick: r.s.tick(),
            kind: 1,
            intent: 0,
            p: &DOG.to_le_bytes(),
        },
    );
    for i in 0..n {
        r.s.on_stream(&mut r.m, &buf[i..i + 1], r.now);
    }
    r.pump();
    assert_eq!(r.s.seq(), 101);
}

#[test]
fn two_frames_in_one_read_both_land() {
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.run_to_tick(1030);
    r.m.clear();
    let t = r.s.tick();
    let mut both = Vec::new();
    let mut buf = [0u8; 256];
    for seq in [101i64, 102] {
        let n = wire::encode_event(
            &mut buf,
            &wire::Event {
                seq,
                tick: t,
                kind: 1,
                intent: 0,
                p: &DOG.to_le_bytes(),
            },
        );
        both.extend_from_slice(&buf[..n]);
    }
    r.s.on_stream(&mut r.m, &both, r.now);
    r.pump();
    assert_eq!(r.s.seq(), 102);
}

#[test]
fn an_unanswered_intent_rides_the_next_connection() {
    // The connection that died never got it to the park, so the next one
    // has to say it again — once, and only after the dog is back.
    let mut r = Rig::boot(ROLE_PLAYER, 1008);
    r.confirm_join();
    r.m.clear();
    let check = r.s.intent_check_in(&mut r.m, r.now);
    assert_eq!(
        r.m.intents(),
        vec![(check, EV_CHECK_IN)],
        "sent on this connection"
    );

    r.s.disconnected();
    r.m.clear();
    r.s.connected(&mut r.m, b"ticket", r.now);
    let rejoin = r.m.intents()[0].0;
    assert!(
        !r.m.intents().iter().any(|i| i.1 == EV_CHECK_IN),
        "the check-in raced the new connection's join"
    );
    r.snapshot(600, 7000, 5, TERRAIN);
    r.pump();
    assert!(
        !r.m.intents().iter().any(|i| i.1 == EV_CHECK_IN),
        "the restore sent it before presence was re-established"
    );
    // the park already holds the dog, so the rejoin comes back present
    r.reject(rejoin, ERR_PRESENT);
    let sent = r.m.intents();
    assert_eq!(
        sent.iter().filter(|i| i.1 == EV_CHECK_IN).count(),
        1,
        "the held intent did not ride the new connection: {sent:?}"
    );
    assert_eq!(sent.iter().find(|i| i.1 == EV_CHECK_IN).unwrap().0, check);
}

#[test]
fn a_restore_never_presents_a_tick_the_viewer_has_passed() {
    // The authority stamps a snapshot where it stood when it took it,
    // which is behind where an optimistic replica has already run to.
    // Presenting that verbatim rewinds the world on screen.
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.run_to_tick(1040);
    let was = r.s.tick();
    r.m.clear();
    r.snapshot(700, was - 3, 9, TERRAIN);
    r.pump();
    assert_eq!(r.s.seq(), 700, "the snapshot did not land");
    assert_eq!(
        r.s.tick(),
        was,
        "the restore left the replica behind where it had already been"
    );
    // a restore from much further back is a real correction, not a hiccup:
    // show it rather than grinding forward through it
    let deep = r.s.tick() - 5 * HZ;
    r.snapshot(800, deep, 3, TERRAIN);
    r.pump();
    assert!(r.s.tick() < was, "a deep correction was ground through");
    assert!(r.s.tick() >= deep);
}

#[test]
fn a_repair_leaves_floors_behind_for_the_next_one() {
    // Without floors recorded on the way back to the present, every repair
    // reaches the same aging cadence entry, so each rewind is longer than
    // the one before it — measured climbing 4, 5, 7, 9 ... 23 ticks for
    // events that were each a single tick late.
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.run_to_tick(1055);
    let was = r.s.tick();

    // the first repair has only the cadence entry to reach for
    r.m.clear();
    let seq = r.s.seq() + 1;
    r.event(seq, was - 1, 3, 0, &DOG.to_le_bytes());
    r.pump();
    let first = r.m.emits_of(T_ROLLBACK)[0];
    assert_eq!(first.0, 1, "late by one tick");
    assert!(first.1 > 2, "this test needs a stale floor to start from");

    // the next ones, over ticks that repair just walked, pay for their own
    // lateness instead of inheriting the first repair's distance
    for round in 0..4 {
        let was = r.s.tick();
        r.m.clear();
        let seq = r.s.seq() + 1;
        r.event(seq, was - 1, 3, 0, &DOG.to_le_bytes());
        r.pump();
        let (late, rewound) = r.m.emits_of(T_ROLLBACK)[0];
        assert_eq!(late, 1);
        assert!(
            rewound <= 2,
            "round {round}: rewound {rewound} for a 1-tick-late event"
        );
        assert_eq!(r.s.tick(), was);
    }
}

#[test]
fn a_journaled_skip_moves_the_clock_with_the_world() {
    // A skip is the authority repaying a long gap: both worlds jump, and
    // nothing is wrong. The replica's model of the authority has to jump
    // with them, because a clock that still believes the old tick sees a
    // replica hours ahead of the server, demands a snapshot, and pays a
    // full download and a visible correction to repair a world that was
    // never broken.
    let mut r = Rig::boot(ROLE_SPECTATOR, 1000);
    for _ in 0..40 {
        r.now += 16;
        r.pump();
        let t = r.s.tick();
        r.verdict_at(t, t + 6, true, true);
    }
    let skip_to = r.s.tick() + 5_000;
    let seq = r.s.seq() + 1;
    r.event(seq, r.s.tick(), EV_CLOCK_SKIP, 0, &skip_to.to_le_bytes());
    r.pump();
    assert_eq!(r.s.tick(), skip_to, "the skip did not land");
    assert_eq!(r.s.tick(), r.m.park.tick, "the core's tick is the park's");

    // and the stream keeps flowing: an event stamped past the destination
    // applies on its own tick like any other
    r.event(seq + 1, skip_to + 4, EV_CHECK_IN, 0, &DOG.to_le_bytes());
    for _ in 0..40 {
        r.now += 16;
        r.pump();
    }
    assert_eq!(r.s.seq(), seq + 1, "the journal stalled behind the skip");
    assert_eq!(
        r.s.stat(STAT_RESYNCS),
        0,
        "a skip is a repayment, not a divergence"
    );
}

/// The authority's own history, computed the way it computes it: the world
/// at entry to a tick carries every event stamped BEFORE that tick and none
/// of the events stamped at it. `park.go` applies a tick's events while its
/// park stands at that tick and rings the hash after stepping, so this is
/// the same rule read from the other side of the wire.
fn reference_hash(base_acc: u64, journal: &[(u64, u16, Vec<u8>)], tick: u64) -> u64 {
    let acc = journal
        .iter()
        .filter(|(t, _, _)| *t < tick)
        .fold(base_acc, |a, (_, kind, p)| {
            let mut ev = kind.to_le_bytes().to_vec();
            ev.extend_from_slice(p);
            a ^ edit_of(&ev)
        });
    toy_hash(tick, acc)
}

/// Every tick the replica recorded a hash for must be the tick the authority
/// would have produced from the same journal. A tick the replica stepped
/// through while already holding an event stamped for it shows up here and
/// nowhere else: the repair a frame later fixes the world, so the end state
/// agrees and only the history disagrees — and the history is what a check
/// datagram samples.
fn assert_history_matches(r: &Rig, base_acc: u64, journal: &[(u64, u16, Vec<u8>)], when: &str) {
    let mut checked = 0;
    for i in 0..HASH_RING {
        let e = bufs().hashes[i];
        if !e.valid || e.tick > r.s.tick() {
            continue;
        }
        assert_eq!(
            e.hash,
            reference_hash(base_acc, journal, e.tick),
            "{when}: the replica's history at tick {} is not the authority's",
            e.tick
        );
        checked += 1;
    }
    assert!(checked > 0, "{when}: no history to compare");
}

#[test]
fn one_journal_two_consumers_agree_at_every_tick() {
    // The differential: one journal through the real session and through the
    // authority's own ordering, compared tick by tick rather than at the
    // end. Comparing end states cannot see a replica that stepped over an
    // event and repaired itself afterwards — the world converges, the
    // history does not, and the history is what the authority answers.
    const BASE: u64 = 7;
    let mut r = Rig::boot(ROLE_SPECTATOR, 6000);
    let mut journal: Vec<(u64, u16, Vec<u8>)> = Vec::new();
    assert_history_matches(&r, BASE, &journal, "at rest");

    // events that arrive before their tick: the ordinary case
    for k in 0..3u64 {
        let at = r.s.tick() + 4;
        let seq = r.s.seq() + 1;
        let p = DOG.wrapping_add(k).to_le_bytes().to_vec();
        r.event(seq, at, EV_CHECK_IN, 0, &p);
        journal.push((at, EV_CHECK_IN, p));
        r.advance(300);
        assert_history_matches(&r, BASE, &journal, "after an event that arrived early");
    }

    // and the adversarial delivery: one late enough to force a repair, one
    // stamped inside the ground that repair walks back over, both landing
    // together
    let was = r.s.tick();
    let seq = r.s.seq();
    let late = DOG.wrapping_add(9).to_le_bytes().to_vec();
    let inside = move_payload(9).to_vec();
    r.event(seq + 1, was - 3, EV_CHECK_IN, 0, &late);
    r.event(seq + 2, was - 1, EV_MOVE_TO, 0, &inside);
    journal.push((was - 3, EV_CHECK_IN, late));
    journal.push((was - 1, EV_MOVE_TO, inside));
    r.pump();
    assert_history_matches(
        &r,
        BASE,
        &journal,
        "after a repair walked back to the present",
    );
    assert_eq!(r.s.tick(), was, "the walk did not return to the present");
}

#[test]
fn a_repair_applies_the_events_it_walks_back_over() {
    // A repair rewinds, fixes the tick the late event belongs to, and then
    // has to walk back to where the viewer was. Anything queued for the
    // ticks it walks over has to land on the way: stepping past one makes
    // it late the moment we arrive, which buys a rollback we caused
    // ourselves — and that one walks too. One late event becomes a train
    // of self-inflicted repairs, each a correction the host has to absorb.
    let mut r = Rig::boot(ROLE_SPECTATOR, 5000);
    r.advance(600);
    let was = r.s.tick();
    let seq = r.s.seq();
    // both arrive in the same delivery: one late enough to force a repair,
    // one stamped inside the ground that repair must walk back over
    r.event(seq + 1, was - 3, EV_CHECK_IN, 0, &DOG.to_le_bytes());
    r.event(seq + 2, was - 1, EV_MOVE_TO, 0, &move_payload(9));
    r.pump();
    assert_eq!(r.s.seq(), seq + 2, "the second event never applied");
    assert_eq!(
        r.s.stat(STAT_ROLLBACKS),
        1,
        "the walk stranded an event and paid for a second repair"
    );
    assert_eq!(r.s.tick(), was, "the walk did not return to the present");
}

#[test]
fn a_verdict_about_history_a_repair_has_replaced_is_not_a_strike() {
    // A check carries the hash the replica held when it asked. If a late
    // event then rewrites that tick, the authority answers "mismatch"
    // about a history this replica has already replaced — true when we
    // asked, stale by the time it lands. Two of those resync a replica
    // that repaired itself correctly, which is the expensive way to be
    // right.
    let mut r = Rig::boot(ROLE_SPECTATOR, 4000);
    r.advance(2000);
    let checked = r.m.checks().last().expect("no check went out").0;
    assert!(checked >= 4000, "the check named a tick before the world");
    r.advance(200);

    // a late event lands at the checked tick: rewind, replay, and the
    // world at that tick is no longer what the check described
    let seq = r.s.seq() + 1;
    r.event(seq, checked, EV_CHECK_IN, 0, &DOG.to_le_bytes());
    r.pump();
    assert_eq!(r.s.stat(STAT_ROLLBACKS), 1, "the repair never happened");

    r.m.clear();
    r.verdict_at(checked, r.s.tick(), true, false);
    assert_eq!(
        r.s.stat(STAT_MISMATCHES),
        0,
        "counted a repair we already made as a divergence"
    );
    assert!(
        r.m.emits_of(T_MISMATCH).is_empty(),
        "reported history we have since replaced as a mismatch"
    );
    r.verdict_at(checked, r.s.tick(), true, false);
    assert!(
        r.m.emits_of(T_RESYNC_REQUESTED).is_empty(),
        "resynced a replica whose repair was correct"
    );

    // and a mismatch about history nothing has rewritten still strikes
    r.advance(6000);
    let fresh = r.m.checks().last().expect("no second check").0;
    r.m.clear();
    r.verdict_at(fresh, r.s.tick(), true, false);
    r.verdict_at(fresh, r.s.tick(), true, false);
    assert_eq!(
        r.m.emits_of(T_RESYNC_REQUESTED).len(),
        1,
        "a real divergence must still resync"
    );
}

#[test]
fn a_check_that_outruns_the_authority_is_not_a_strike() {
    // Right after a restore the replica sits at the snapshot's tick, ahead
    // of its own lag target, so its checks name ticks the authority has
    // not stamped yet. Its honest "I have never heard of that tick" is not
    // evidence of staleness, and treating it as such resyncs a replica
    // that is perfectly correct.
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.advance(1000);
    r.m.clear();
    let ahead = r.s.tick();
    for _ in 0..6 {
        r.verdict_at(ahead, ahead - 6, false, false);
    }
    assert!(
        r.m.emits_of(T_RESYNC_REQUESTED).is_empty(),
        "a replica racing the authority's present resynced itself"
    );
    assert_eq!(r.s.stat(STAT_RESYNCS), 0);

    // a genuinely stale check — a tick far below where the authority now
    // stands — still strikes twice and resyncs
    r.verdict_at(1, ahead, false, false);
    r.verdict_at(1, ahead, false, false);
    assert_eq!(
        r.m.emits_of(T_RESYNC_REQUESTED),
        vec![(R_CHECK_AGED_OUT as u64, 100)]
    );
}
