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

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
enum Verb {
    Apply(u32, u16),
    Step(u32),
    Snapshot(u32),
    Restore(u32),
}

// A recording stand-in for the park module: enough state that rollback,
// restore, and hashing mean something (tick plus a fold of every applied
// event), and no game rules at all.
#[derive(Clone, Copy)]
struct Toy {
    tick: u64,
    acc: u64,
}

const TOY_BYTES: usize = 18;

fn toy_state(tick: u64, acc: u64) -> [u8; TOY_BYTES] {
    let mut b = [0u8; TOY_BYTES];
    b[..2].copy_from_slice(b"TP");
    b[2..10].copy_from_slice(&tick.to_le_bytes());
    b[10..18].copy_from_slice(&acc.to_le_bytes());
    b
}

fn toy_hash(tick: u64, acc: u64) -> u64 {
    let mut z = tick
        .wrapping_mul(0x9E37_79B9_7F4A_7C15)
        .wrapping_add(acc.wrapping_mul(0xBF58_476D_1CE4_E5B9));
    z = (z ^ (z >> 30)).wrapping_mul(0x94D0_49BB_1331_11EB);
    z ^ (z >> 31)
}

struct Mock {
    parks: [Toy; 2],
    verbs: Vec<Verb>,
    sent: Vec<Vec<u8>>,
    dgs: Vec<Vec<u8>>,
    reqs: Vec<(u32, u64)>,
    emits: Vec<(u32, u64, u64)>,
    restore_code: u32,
    reject: Option<(u32, u16, u32)>,
}

impl Mock {
    fn new() -> Mock {
        Mock {
            parks: [Toy { tick: 0, acc: 0 }; 2],
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

    fn applies(&self, slot: u32) -> Vec<u16> {
        self.verbs
            .iter()
            .filter_map(|v| match v {
                Verb::Apply(s, k) if *s == slot => Some(*k),
                _ => None,
            })
            .collect()
    }

    fn count(&self, want: Verb) -> usize {
        self.verbs.iter().filter(|v| **v == want).count()
    }
}

impl Host for Mock {
    fn park_apply(&mut self, slot: u32, ev: &[u8]) -> u32 {
        let kind = u16::from_le_bytes([ev[0], ev[1]]);
        self.verbs.push(Verb::Apply(slot, kind));
        if let Some((s, k, code)) = self.reject
            && s == slot
            && k == kind
        {
            return code;
        }
        let p = &mut self.parks[slot as usize];
        p.acc = toy_hash(p.acc ^ kind as u64, ev.len() as u64);
        0
    }

    fn park_step(&mut self, slot: u32) {
        self.verbs.push(Verb::Step(slot));
        self.parks[slot as usize].tick += 1;
    }

    fn park_snapshot(&mut self, slot: u32, dst: &mut [u8]) -> u32 {
        self.verbs.push(Verb::Snapshot(slot));
        let p = self.parks[slot as usize];
        dst[..TOY_BYTES].copy_from_slice(&toy_state(p.tick, p.acc));
        TOY_BYTES as u32
    }

    fn park_restore(&mut self, slot: u32, state: &[u8]) -> u32 {
        self.verbs.push(Verb::Restore(slot));
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
        self.parks[slot as usize] = Toy {
            tick: u64::from_le_bytes(t),
            acc: u64::from_le_bytes(a),
        };
        0
    }

    fn park_hash(&mut self, slot: u32) -> u64 {
        let p = self.parks[slot as usize];
        toy_hash(p.tick, p.acc)
    }

    fn park_tick(&mut self, slot: u32) -> u64 {
        self.parks[slot as usize].tick
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

impl Rig {
    fn new(role: u32) -> Rig {
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
    assert_eq!(r.m.applies(SLOT_JOURNAL), vec![1, 3, 3]);
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
    assert_eq!(r.m.count(Verb::Restore(SLOT_JOURNAL)), 1, "one rollback");
    assert_eq!(r.m.emits_of(T_ROLLBACK), vec![(was - 1032, 1032)]);
    assert_eq!(r.s.stat(STAT_ROLLBACKS), 1);
    assert_eq!(r.s.seq(), 102);
    assert_eq!(r.s.tick(), late, "the late event applies where it belongs");
    // the event since the ring entry was replayed, then the late one landed
    assert_eq!(r.m.applies(SLOT_JOURNAL), vec![1, 3]);
    // and the deficit the rewind opened is just stepping to the clock
    r.advance(1000);
    assert!(r.s.tick() > was);
    assert_eq!(r.s.stat(STAT_RESYNCS), 0);
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
    assert_eq!(entry, toy_hash(t, r.m.parks[0].acc));
    // apply an event stamped for this very tick, without leaving it
    r.event(101, t, 1, 0, &DOG.to_le_bytes());
    r.pump();
    assert_eq!(r.s.tick(), t, "wall time did not move");
    assert_eq!(r.s.seq(), 101);
    assert_ne!(
        toy_hash(t, r.m.parks[0].acc),
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
    assert_eq!(r.m.count(Verb::Restore(SLOT_JOURNAL)), 0, "restored blind");
    assert_eq!(r.m.reqs_of(REQ_NEED_TERRAIN), vec![TERRAIN]);
    assert!(!r.s.have_state);
    r.s.terrain_ready(&mut r.m, TERRAIN, true);
    r.pump();
    assert_eq!(r.m.count(Verb::Restore(SLOT_JOURNAL)), 1);
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
    let id = r.s.intent_check_in(&mut r.m, r.now);
    r.event(400, 5000, 1, 0, &DOG.to_le_bytes());
    r.m.clear();
    r.snapshot(300, 4000, 11, TERRAIN);
    r.pump();
    assert_eq!(r.s.seq(), 300);
    assert_eq!(r.s.tick(), 4000);
    assert_eq!(r.s.queued_len, 0, "the queue does not survive a restore");
    assert_eq!(r.s.ring_len, 0);
    assert_eq!(r.s.recent_len, 0);
    // the join from connect() and the check-in both ride again
    let resent = r.m.intents();
    assert_eq!(resent.len(), 2);
    assert!(resent.contains(&(id, EV_CHECK_IN)));
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
    assert_eq!(
        r.m.count(Verb::Restore(SLOT_JOURNAL)),
        1,
        "tried exactly once"
    );
    assert_eq!(
        r.m.emits_of(T_RESTORE_FAILED),
        vec![(RESTORE_WRONG_TERRAIN as u64, 300)]
    );
    r.advance(30_000);
    assert_eq!(
        r.m.count(Verb::Restore(SLOT_JOURNAL)),
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
    assert_eq!(
        r.m.count(Verb::Step(SLOT_JOURNAL)),
        0,
        "no stepping without a world"
    );
    r.snapshot(700, 9000, 3, TERRAIN);
    r.pump();
    assert!(r.s.have_state);
    assert_eq!(r.s.tick(), 9000);
}

// ---- invariant 8: the prediction overlay ----

#[test]
fn pending_intents_ride_on_the_presented_slot_until_the_journal_answers() {
    let mut r = Rig::boot(ROLE_PLAYER, 1008);
    // connect() sent the join, the restore resent it, and the overlay
    // carries it until the journal answers
    let joined = r.m.intents();
    assert_eq!(joined.len(), 2, "sent on connect, resent after the restore");
    assert!(joined.iter().all(|i| *i == joined[0]));
    assert_eq!(joined[0].1, EV_JOIN);
    r.m.clear();
    let id = r.s.intent_move_to(&mut r.m, 42, r.now);
    assert_eq!(
        id >> 32,
        NONCE as u64,
        "intent ids carry the page-load nonce"
    );
    r.pump();
    assert_eq!(
        r.m.count(Verb::Restore(SLOT_PRESENTED)),
        1,
        "overlay rebuilt"
    );
    assert_eq!(r.m.applies(SLOT_PRESENTED), vec![EV_JOIN, EV_MOVE_TO]);
    assert!(
        r.m.applies(SLOT_JOURNAL).is_empty(),
        "predictions never touch slot 0"
    );

    // the journal answers the move: the prediction stops being an overlay
    r.m.clear();
    r.event(101, r.s.tick(), EV_MOVE_TO, id, &[0u8; 10]);
    r.pump();
    assert_eq!(r.m.applies(SLOT_JOURNAL), vec![EV_MOVE_TO]);
    assert_eq!(r.m.applies(SLOT_PRESENTED), vec![EV_JOIN]);
    assert_eq!(r.s.intents_len, 1);
}

#[test]
fn only_an_exact_intent_id_acknowledges_a_pending_intent() {
    // The wire dropped the actor, so the intent id is the only handle back
    // to a sender. A bare counter would collide across senders and page
    // loads; these two ids share a counter and differ in nonce, and the
    // foreign one must not touch our pending set or our overlay.
    let mut r = Rig::boot(ROLE_PLAYER, 1008);
    r.m.clear();
    let mine = r.s.intent_move_to(&mut r.m, 42, r.now);
    assert_eq!(mine >> 32, NONCE as u64);
    let theirs = ((NONCE as u64 ^ 0xFFFF) << 32) | (mine & 0xFFFF_FFFF);
    assert_ne!(mine, theirs);
    let before = r.s.intents_len;
    r.event(101, r.s.tick(), EV_MOVE_TO, theirs, &[0u8; 10]);
    r.pump();
    assert_eq!(
        r.s.intents_len, before,
        "a foreign event cleared our intent"
    );
    assert_eq!(r.m.applies(SLOT_PRESENTED), vec![EV_JOIN, EV_MOVE_TO]);
    // and the real acknowledgment still lands
    r.m.clear();
    r.event(102, r.s.tick(), EV_MOVE_TO, mine, &[0u8; 10]);
    r.pump();
    assert_eq!(r.s.intents_len, before - 1);
    assert_eq!(r.m.applies(SLOT_PRESENTED), vec![EV_JOIN]);
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
fn the_overlay_rebuilds_after_every_slot_zero_mutation() {
    let mut r = Rig::boot(ROLE_PLAYER, 1008);
    r.run_to_tick(1030);
    r.m.clear();
    r.advance(500);
    let idle_rebuilds = r.m.count(Verb::Restore(SLOT_PRESENTED));
    assert_eq!(idle_rebuilds, 0, "lockstep stepping needs no rebuild");
    let steps0 = r.m.count(Verb::Step(SLOT_JOURNAL));
    assert_eq!(
        r.m.count(Verb::Step(SLOT_PRESENTED)),
        steps0,
        "both slots step in lockstep"
    );
    r.m.clear();
    r.event(101, r.s.tick(), EV_CHECK_IN, 0, &DOG.to_le_bytes());
    r.pump();
    assert_eq!(r.m.count(Verb::Restore(SLOT_PRESENTED)), 1);
}

#[test]
fn an_overlay_reject_is_silent() {
    let mut r = Rig::boot(ROLE_PLAYER, 1008);
    r.m.reject = Some((SLOT_PRESENTED, EV_BOOST_SET, ERR_NOOP));
    r.m.clear();
    r.s.intent_boost(&mut r.m, true, r.now);
    r.pump();
    r.pump();
    assert_eq!(r.m.applies(SLOT_PRESENTED), vec![EV_JOIN, EV_BOOST_SET]);
    assert!(r.m.emits_of(T_RESYNC_REQUESTED).is_empty());
    assert!(r.m.emits_of(T_REJECT).is_empty());
    assert_eq!(r.s.stat(STAT_RESYNCS), 0);
}

#[test]
fn an_event_the_replica_refuses_is_a_resync() {
    let mut r = Rig::boot(ROLE_SPECTATOR, 1008);
    r.run_to_tick(1030);
    r.m.reject = Some((SLOT_JOURNAL, EV_CHECK_IN, 5));
    r.m.clear();
    r.event(101, r.s.tick(), EV_CHECK_IN, 0, &DOG.to_le_bytes());
    r.pump();
    assert_eq!(
        r.m.emits_of(T_RESYNC_REQUESTED),
        vec![(R_EVENT_REJECTED as u64, 100)]
    );
    assert_eq!(r.s.seq(), 100, "a refused event never advances seq");
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
    r.m.clear();
    let id = r.s.intent_boost(&mut r.m, true, r.now);
    r.reject(id, ERR_NOOP);
    assert!(r.m.emits_of(T_REJECT).is_empty());
}

#[test]
fn absent_on_a_players_intent_rejoins_the_park_once_per_window() {
    let mut r = Rig::boot(ROLE_PLAYER, 1008);
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
    r.m.clear();
    let id2 = r.s.intent_check_in(&mut r.m, r.now);
    r.reject(id2, ERR_ABSENT);
    assert!(r.m.emits_of(T_AUTO_REJOIN).is_empty());
    // past the window it does
    r.now += 6000;
    r.m.clear();
    let id3 = r.s.intent_check_in(&mut r.m, r.now);
    r.reject(id3, ERR_ABSENT);
    assert_eq!(r.m.emits_of(T_AUTO_REJOIN).len(), 1);
}

#[test]
fn a_rejected_intent_leaves_the_overlay_immediately() {
    // A refused intent that stays predicted is a ghost: in a quiet park no
    // later event would mark the overlay dirty, so slot 1 would show the
    // authority's refusal as reality forever. Every reject that removes a
    // pending entry must schedule the rebuild — including the two the core
    // swallows, which remove it just the same.
    for reason in [101, ERR_PRESENT] {
        let mut r = Rig::boot(ROLE_PLAYER, 1008);
        r.pump();
        let join = r.m.intents()[0].0;
        r.m.clear();
        r.reject(join, reason);
        r.pump();
        assert_eq!(r.s.intents_len, 0, "reason {reason}");
        assert_eq!(
            r.m.count(Verb::Restore(SLOT_PRESENTED)),
            1,
            "reason {reason}: overlay not rebuilt"
        );
        assert!(
            r.m.applies(SLOT_PRESENTED).is_empty(),
            "reason {reason}: the refused intent is still predicted"
        );
    }
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
        r.m.count(Verb::Step(SLOT_JOURNAL)),
        0,
        "slot 0 stepped under a zero budget"
    );
    assert_eq!(r.m.count(Verb::Step(SLOT_PRESENTED)), 0, "slot 1 stepped");
    assert!(states.contains(&2), "never escalated to fast-forward");
    assert_eq!(
        *states.last().unwrap(),
        3,
        "never reached snapshot-required"
    );
    assert_eq!(
        r.m.emits_of(T_RESYNC_REQUESTED),
        vec![(R_CLOCK as u64, 100)],
        "a starved replica must ask for a snapshot exactly once"
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
    assert_eq!(r.m.applies(SLOT_JOURNAL), vec![1]);
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
