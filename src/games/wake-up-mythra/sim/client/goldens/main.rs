//! Mints the record-layout goldens (`goldens/records.json`): known-value
//! HUD, diagnostics, view, and terrain records written by the real Rust
//! writers, as hex plus their decoded fields. The TypeScript decoders are
//! tested against these bytes, so offsets and widths are pinned across the
//! language boundary rather than trusted. Committed and diff-tested like
//! the wasm artifacts; refreshed by the same target.

use mythra_sim_park as park;
use mythra_sim_terrain::{BYTES_PER_CELL, Builder, HEADER, SWIM};
use wum_session::wire::{self, Snapshot, Verdict, Welcome};
use wum_session::{Host, ROLE_SPECTATOR, Session};

fn hex(bytes: &[u8]) -> String {
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}

fn u16at(b: &[u8], at: usize) -> u16 {
    u16::from_le_bytes(b[at..at + 2].try_into().unwrap())
}

fn u32at(b: &[u8], at: usize) -> u32 {
    u32::from_le_bytes(b[at..at + 4].try_into().unwrap())
}

fn u64at(b: &[u8], at: usize) -> u64 {
    u64::from_le_bytes(b[at..at + 8].try_into().unwrap())
}

fn i32at(b: &[u8], at: usize) -> i32 {
    i32::from_le_bytes(b[at..at + 4].try_into().unwrap())
}

fn i64at(b: &[u8], at: usize) -> i64 {
    i64::from_le_bytes(b[at..at + 8].try_into().unwrap())
}

// ---- the park under golden state -----------------------------------------

fn park_apply(kind: u16, actor: u64, payload: &[u8]) -> u32 {
    let mut ev = kind.to_le_bytes().to_vec();
    ev.extend_from_slice(&actor.to_le_bytes());
    ev.extend_from_slice(payload);
    unsafe {
        std::ptr::copy_nonoverlapping(ev.as_ptr(), park::io_buf(), ev.len());
    }
    park::sim_apply(ev.len() as u32)
}

fn park_io(len: usize) -> Vec<u8> {
    let mut out = vec![0u8; len];
    unsafe {
        std::ptr::copy_nonoverlapping(park::io_buf() as *const u8, out.as_mut_ptr(), len);
    }
    out
}

/// All-WALK terrain, so the spawn cells cannot be swim or deck and the
/// golden dog's view flags are exactly its boost bit.
fn setup_park() {
    let mut buf = vec![0u8; HEADER + 144 * BYTES_PER_CELL];
    let blob = Builder::new(&mut buf, 12, 12).finish();
    assert_eq!(park::stage_content(blob), 0);
    assert_eq!(park::sim_init(3, 42, 1), 0);
    assert_eq!(park_apply(park::EV_JOIN, 8, &[]), 0);
    assert_eq!(park_apply(park::EV_JOIN, 9, &[]), 0);
    assert_eq!(park_apply(park::EV_DAY_RESET, 0, &4u32.to_le_bytes()), 0);
    assert_eq!(park_apply(park::EV_CHECK_IN, 8, &[]), 0);
    assert_eq!(park_apply(park::EV_BOOST_SET, 8, &[1]), 0);
}

fn hud_golden() -> String {
    let len = park::sim_hud(8) as usize;
    assert_eq!(len, 28);
    let b = park_io(len);
    assert_eq!(u16at(&b, 0), 1);
    assert_eq!((b[2], b[3]), (1, 1));
    assert_eq!(u32at(&b, 4), 4);
    assert_eq!(u32at(&b, 8), 2);
    format!(
        concat!(
            "{{\n",
            "    \"hex\": \"{}\",\n",
            "    \"present\": {},\n",
            "    \"checkedInToday\": {},\n",
            "    \"day\": {},\n",
            "    \"dogCount\": {},\n",
            "    \"parkEnergy\": \"{}\",\n",
            "    \"selfEnergy\": {},\n",
            "    \"boosting\": {}\n",
            "  }}"
        ),
        hex(&b),
        b[2] != 0,
        b[3] != 0,
        u32at(&b, 4),
        u32at(&b, 8),
        u64at(&b, 12),
        u32at(&b, 20),
        b[24] & 2 != 0,
    )
}

fn view_golden() -> String {
    let len = park::sim_view() as usize;
    assert_eq!(len, 4 + 2 * 20);
    let b = park_io(len);
    assert_eq!(u32at(&b, 0), 2);
    let r = &b[4..24];
    assert_eq!(u64at(r, 0), 8);
    assert_eq!(r[16], 8, "boost only: all-WALK terrain, standing");
    assert_eq!((r[17], r[18], r[19]), (2, 0, 0));
    format!(
        concat!(
            "{{\n",
            "    \"hex\": \"{}\",\n",
            "    \"count\": {},\n",
            "    \"dog\": {{\n",
            "      \"id\": \"{}\",\n",
            "      \"x\": {},\n",
            "      \"y\": {},\n",
            "      \"flags\": {},\n",
            "      \"facing\": {},\n",
            "      \"anim\": {}\n",
            "    }}\n",
            "  }}"
        ),
        hex(&b),
        u32at(&b, 0),
        u64at(r, 0),
        i32at(r, 8),
        i32at(r, 12),
        r[16],
        r[17],
        r[18],
    )
}

fn terrain_golden() -> String {
    let mut buf = vec![0u8; HEADER + 48 * BYTES_PER_CELL];
    let mut b = Builder::new(&mut buf, 8, 6);
    b.set_ground(2, 1, SWIM, 0);
    b.set_obstacle(3, 2, 0b0000_0110_0110_0000);
    b.set_variant(5, 4, 3);
    let blob = b.finish();
    format!(
        concat!(
            "{{\n",
            "    \"hex\": \"{}\",\n",
            "    \"w\": 8,\n",
            "    \"h\": 6,\n",
            "    \"swimCell\": {{ \"x\": 2, \"y\": 1 }},\n",
            "    \"obstacle\": {{ \"x\": 3, \"y\": 2, \"mask\": {} }},\n",
            "    \"variant\": {{ \"x\": 5, \"y\": 4, \"value\": 3 }}\n",
            "  }}"
        ),
        hex(blob),
        0b0000_0110_0110_0000,
    )
}

// ---- the session under golden state --------------------------------------

const PW: u32 = 0x1122_3344;
const TERRAIN_ID: u64 = 0xAABB_CCDD_0011_2233;
const RTT_MS: u64 = 120;

fn toy_hash(tick: u64) -> u64 {
    tick.wrapping_mul(0x9E37_79B9_7F4A_7C15) | 1
}

/// A tick-counting park and a recording transport: just enough host for
/// the session to restore, step, apply, and answer checks.
struct Toy {
    tick: u64,
    datagrams: Vec<Vec<u8>>,
}

impl Host for Toy {
    fn park_apply(&mut self, _ev: &[u8]) -> u32 {
        0
    }
    fn park_step(&mut self) {
        self.tick += 1;
    }
    fn park_snapshot(&mut self, dst: &mut [u8]) -> u32 {
        dst[..8].copy_from_slice(&self.tick.to_le_bytes());
        8
    }
    fn park_restore(&mut self, state: &[u8]) -> u32 {
        self.tick = u64at(state, 0);
        0
    }
    fn park_hash(&mut self) -> u64 {
        toy_hash(self.tick)
    }
    fn park_tick(&mut self) -> u64 {
        self.tick
    }
    fn send_stream(&mut self, _frame: &[u8]) {}
    fn send_datagram(&mut self, datagram: &[u8]) {
        self.datagrams.push(datagram.to_vec());
    }
    fn inflate(&mut self, src: &[u8], dst: &mut [u8]) -> u32 {
        dst[..src.len()].copy_from_slice(src);
        src.len() as u32
    }
    fn request(&mut self, _kind: u32, _a: u64) {}
    fn emit(&mut self, _kind: u32, _a: u64, _b: u64) {}
}

fn diag_golden() -> String {
    let mut session = Session::NEW;
    let toy = &mut Toy {
        tick: 0,
        datagrams: Vec::new(),
    };
    let t0: u64 = 1_000_000;
    session.init(0x0D06, ROLE_SPECTATOR, 1000, 0xC0FF_EE01, t0);
    session.module_swapped(toy, PW);
    assert_eq!(session.connected(toy, b"golden-ticket", t0), 0);

    let mut frame = [0u8; 4096];
    let n = wire::encode_welcome(
        &mut frame,
        &Welcome {
            lineage: 0,
            generation: 0,
            sub: 0,
            epoch: 3,
            seq: 40,
            tick: 960,
            hz: 24,
            role: 0,
            content: TERRAIN_ID,
            chunk: b"golden",
        },
    );
    session.on_stream(toy, &frame[..n], t0);
    session.terrain_ready(toy, TERRAIN_ID, true);
    let n = wire::encode_snapshot(
        &mut frame,
        &Snapshot {
            lineage: 0,
            seq: 40,
            tick: 960,
            epoch: 3,
            wh: toy_hash(960),
            content: TERRAIN_ID,
            z: &960u64.to_le_bytes(),
        },
    );
    session.on_stream(toy, &frame[..n], t0 + 40);

    // The authority's schedule: tick 960 at the moment the snapshot landed,
    // 24Hz thereafter. Verdicts and event stamps follow it, so the clock
    // disciplines against an authority that keeps real time.
    let auth = |t: u64| 960 + (t - (t0 + 40)) * 24 / 1000;
    let mut events_sent = false;
    let mut answered = 0;
    let mut inbox: Vec<(u64, Vec<u8>)> = Vec::new();
    let mut flags = 0;
    let mut t = t0 + 40;
    let t_end = t0 + 3040;
    while t <= t_end {
        flags = session.pump(toy, t, 8_000);
        if !events_sent && toy.tick >= 975 {
            events_sent = true;
            for (seq, kind, p) in [(41i64, 3u16, &[][..]), (42, 8, &[1u8][..])] {
                let mut run = [0u8; 64];
                let rn = wire::put_record(&mut run, 0, kind, 8, p);
                let n =
                    wire::encode_tick(&mut frame, auth(t) + (seq - 40) as u64, seq, 1, &run[..rn]);
                session.on_stream(toy, &frame[..n], t);
            }
        }
        while answered < toy.datagrams.len() {
            let check = wire::parse_check(&toy.datagrams[answered]).expect("a check datagram");
            answered += 1;
            let minted = t + RTT_MS / 2;
            let n = wire::encode_verdict(
                &mut frame,
                &Verdict {
                    sub: 0,
                    lineage: 0,
                    tick: check.tick,
                    now: auth(minted),
                    ct_ms: check.ct_ms,
                    flags: wire::VERDICT_KNOWN | wire::VERDICT_OK,
                    cw: [0; 4],
                    pw: PW.to_le_bytes(),
                },
            );
            inbox.push((t + RTT_MS, frame[..n].to_vec()));
        }
        inbox.retain(|(due, datagram)| {
            if *due > t {
                return true;
            }
            session.on_datagram(toy, datagram, t);
            false
        });
        t += 20;
    }
    let t_end = t - 20;

    let mut b = [0u8; 96];
    assert_eq!(session.diag(t_end, &mut b), 96);
    assert_eq!(u16at(&b, 0), 1);
    assert_eq!(b[2] as u32, (flags >> 8) & 3);
    assert_eq!(u32at(&b, 4), session.rtt_ms());
    assert_eq!(i64at(&b, 8), session.trail_q16(t_end));
    assert_eq!(i64at(&b, 16), session.error_q16(t_end));
    assert_eq!(u64at(&b, 24), session.tick());
    assert_eq!(i64at(&b, 32), session.seq());
    assert_eq!(session.seq(), 42, "both events applied");
    assert!(session.stat(1) >= 2 && session.stat(4) >= 1);
    for stat in 1..=6u32 {
        assert_eq!(u64at(&b, 40 + stat as usize * 8), session.stat(stat));
    }
    format!(
        concat!(
            "{{\n",
            "    \"hex\": \"{}\",\n",
            "    \"clockState\": {},\n",
            "    \"rttMs\": {},\n",
            "    \"trailQ16\": \"{}\",\n",
            "    \"errorQ16\": \"{}\",\n",
            "    \"tick\": \"{}\",\n",
            "    \"seq\": \"{}\",\n",
            "    \"trailTargetTicks\": {},\n",
            "    \"cushionTicks\": {},\n",
            "    \"events\": {},\n",
            "    \"rollbacks\": {},\n",
            "    \"resyncs\": {},\n",
            "    \"checks\": {},\n",
            "    \"mismatches\": {},\n",
            "    \"rejects\": {}\n",
            "  }}"
        ),
        hex(&b),
        b[2],
        session.rtt_ms(),
        session.trail_q16(t_end),
        session.error_q16(t_end),
        session.tick(),
        session.seq(),
        u32at(&b, 40),
        u32at(&b, 44),
        session.stat(1),
        session.stat(2),
        session.stat(3),
        session.stat(4),
        session.stat(5),
        session.stat(6),
    )
}

fn main() {
    let path = std::env::args().nth(1).expect("usage: goldens <out-path>");
    setup_park();
    let out = format!(
        "{{\n  \"hud\": {},\n  \"diag\": {},\n  \"view\": {},\n  \"terrain\": {}\n}}\n",
        hud_golden(),
        diag_golden(),
        view_golden(),
        terrain_golden(),
    );
    std::fs::write(path, out).expect("write records.json");
}
