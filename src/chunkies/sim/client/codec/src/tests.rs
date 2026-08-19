//! Conformance against the shared spec. The vectors file is compiled in
//! from its canonical location so this suite cannot drift from the Go and
//! TS suites reading the same bytes.

use super::*;

const VECTORS: &str = include_str!("../../../../codec/spec/vectors.txt");
const CAPS: &str = include_str!("../../../../codec/spec/caps.txt");

// The shared fixture values behind every vector.
const FX_LINEAGE: u32 = 7;
const FX_GENERATION: u32 = 3;
const FX_SUB: u32 = 0x0D0C0B0A;
const FX_EPOCH: u32 = 2;
const FX_SEQ: i64 = 9;
const FX_TICK: u64 = 1024;
const FX_HZ: u32 = 24;
const FX_WH: u64 = 0xFEED_FACE_CAFE_BEEF;
const FX_CONTENT: u64 = 0x0123_4567_89AB_CDEF;
const FX_CT: u64 = 1_700_000_000_000;
const FX_INTENT: u64 = 0x1122_3344_5566_7788;
const FX_ACTOR: u64 = 0xA1A2_A3A4_A5A6_A7A8;
const FX_KIND: u16 = 0x0104;
const FX_SYS_KIND: u16 = 0x0009;

// Record vectors are authority-side (write-ahead log, checkpoints) and
// implemented in Go only; a Rust authority promotes them into this crate.
const GO_ONLY: &[&str] = &["segment", "tickrec", "watermark", "checkpoint"];
const GO_ONLY_CAPS: &[&str] = &["WAL_MAX_RECORD", "WAL_MAX_CHUNKS"];

fn unhex(s: &str) -> Vec<u8> {
    assert!(s.len() % 2 == 0, "odd hex length");
    (0..s.len())
        .step_by(2)
        .map(|i| u8::from_str_radix(&s[i..i + 2], 16).expect("bad hex"))
        .collect()
}

fn vectors() -> (Vec<(String, Vec<u8>)>, Vec<(String, Vec<u8>)>) {
    let (mut good, mut bad) = (Vec::new(), Vec::new());
    for line in VECTORS.lines() {
        let line = line.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        let (name, hex) = line.split_once(" = ").expect("unparseable vector line");
        match name.strip_prefix('!') {
            Some(n) => bad.push((n.to_string(), unhex(hex))),
            None => good.push((name.to_string(), unhex(hex))),
        }
    }
    (good, bad)
}

fn fx_run(dst: &mut [u8]) -> usize {
    let n = put_record(dst, FX_INTENT, FX_KIND, FX_ACTOR, &[0xDE, 0xAD, 0xBE, 0xEF]);
    n + put_record(
        &mut dst[n..],
        SYSTEM_INTENT,
        FX_SYS_KIND,
        0,
        &[0x0A, 0x0B, 0x0C, 0x0D],
    )
}

/// Encodes this implementation's fixture message for a vector name, or
/// None for names this crate does not implement.
fn encode(name: &str) -> Option<Vec<u8>> {
    let mut buf = [0u8; 4096];
    let n = match name {
        "hello" => encode_hello(&mut buf, FX_LINEAGE, FX_SEQ, FX_TICK, b"T-9f"),
        "intent" => encode_intent(
            &mut buf,
            FX_INTENT,
            FX_KIND,
            FX_ACTOR,
            &[0xDE, 0xAD, 0xBE, 0xEF],
        ),
        "resync" => encode_resync(&mut buf, FX_LINEAGE, FX_SEQ),
        "resync-neg" => encode_resync(&mut buf, FX_LINEAGE, -1),
        "welcome" => encode_welcome(
            &mut buf,
            &Welcome {
                lineage: FX_LINEAGE,
                generation: FX_GENERATION,
                sub: FX_SUB,
                epoch: FX_EPOCH,
                seq: FX_SEQ,
                tick: FX_TICK,
                hz: FX_HZ,
                role: 1,
                content: FX_CONTENT,
                chunk: b"park-a",
            },
        ),
        "tick" => {
            let mut run = [0u8; 128];
            let rn = fx_run(&mut run);
            encode_tick(&mut buf, FX_TICK, FX_SEQ, 2, &run[..rn])
        }
        "reject" => encode_reject(&mut buf, FX_INTENT, 101),
        "snapshot" => encode_snapshot(
            &mut buf,
            &Snapshot {
                lineage: FX_LINEAGE,
                seq: FX_SEQ,
                tick: FX_TICK,
                epoch: FX_EPOCH,
                wh: FX_WH,
                content: FX_CONTENT,
                z: &[0xC0, 0xFF, 0xEE, 0x00],
            },
        ),
        "check" => encode_check(&mut buf, FX_SUB, FX_TICK, FX_WH, FX_CT),
        "verdict" => encode_verdict(
            &mut buf,
            &Verdict {
                sub: FX_SUB,
                lineage: FX_LINEAGE,
                tick: FX_TICK,
                now: FX_CT + 123,
                ct_ms: FX_CT,
                flags: VERDICT_KNOWN | VERDICT_OK,
                cw: [0x9A, 0xBC, 0xDE, 0xF0],
                pw: [0x12, 0x34, 0x56, 0x78],
            },
        ),
        _ => return None,
    };
    Some(buf[..n].to_vec())
}

/// Strictly decodes a full vector (frames: prefix + kind + payload), for
/// names this crate implements.
fn decodes(name: &str, raw: &[u8]) -> Option<bool> {
    if name == "check" {
        return Some(parse_check(raw).is_some());
    }
    if name == "verdict" {
        return Some(parse_verdict(raw).is_some());
    }
    let Ok(Some((kind, at, plen, total))) = frame_bounds(raw) else {
        return Some(false);
    };
    if total != raw.len() {
        return Some(false);
    }
    let p = &raw[at..at + plen];
    let ok = match name {
        "hello" => parse_hello(p).is_some_and(|h| h.proto == PROTO) && kind == K_HELLO,
        "intent" => parse_intent(p).is_some() && kind == K_INTENT,
        "resync" | "resync-neg" => parse_resync(p).is_some() && kind == K_RESYNC,
        "welcome" => parse_welcome(p).is_some() && kind == K_WELCOME,
        "tick" => parse_tick(p).is_some() && kind == K_TICK,
        "reject" => parse_reject(p).is_some() && kind == K_REJECT,
        "snapshot" => parse_snapshot(p).is_some() && kind == K_SNAPSHOT,
        _ => return None,
    };
    Some(ok)
}

#[test]
fn every_vector_encodes_and_decodes() {
    let (good, bad) = vectors();
    assert!(!good.is_empty());
    for (name, want) in &good {
        if GO_ONLY.contains(&name.as_str()) {
            continue;
        }
        let got = encode(name).unwrap_or_else(|| panic!("vector {name} has no encoder"));
        assert_eq!(
            got, *want,
            "{name}: encoded bytes disagree with the spec vector"
        );
        assert_eq!(
            decodes(name, want),
            Some(true),
            "{name}: spec vector failed to decode"
        );
    }
    for (name, raw) in &bad {
        let base = name.split('-').next().unwrap();
        if GO_ONLY.contains(&base) {
            continue;
        }
        assert_eq!(
            decodes(base, raw),
            Some(false),
            "!{name}: malformed vector unexpectedly decoded"
        );
    }
}

#[test]
fn caps_match_the_spec() {
    let mine: &[(&str, usize)] = &[
        ("PROTO", PROTO as usize),
        ("MAX_FRAME", MAX_FRAME),
        ("DG_MAX", DG_MAX),
    ];
    let mut seen = 0;
    for line in CAPS.lines() {
        let line = line.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        let (name, val) = line.split_once('=').expect("unparseable caps line");
        if GO_ONLY_CAPS.contains(&name) {
            continue;
        }
        let val: usize = val.parse().expect("caps value");
        let (_, have) = mine
            .iter()
            .find(|(n, _)| *n == name)
            .unwrap_or_else(|| panic!("caps.txt names {name}, undefined here"));
        assert_eq!(*have, val, "{name} disagrees with caps.txt");
        seen += 1;
    }
    assert_eq!(seen, mine.len(), "caps.txt does not pin every constant");
}

#[test]
fn intent_record_is_a_verbatim_slice_of_the_tick() {
    let (good, _) = vectors();
    let get = |n: &str| &good.iter().find(|(name, _)| name == n).unwrap().1;
    let (intent, tick) = (get("intent"), get("tick"));
    let (_, at, plen, _) = frame_bounds(intent).unwrap().unwrap();
    let rec = &intent[at..at + plen];
    assert!(
        tick.windows(rec.len()).any(|w| w == rec),
        "the intent record must appear verbatim inside the tick frame"
    );
}

#[test]
fn frame_bounds_enforces_the_shared_cap() {
    // v4 shipped this check Go-only; the caps table exists so this cannot
    // regress. A declared body over MAX_FRAME is a dead connection.
    let mut buf = [0u8; 16];
    put_varint(&mut buf, MAX_FRAME as u64 + 1);
    assert_eq!(frame_bounds(&buf), Err(()));
    // At the cap it is only "need more bytes".
    put_varint(&mut buf, MAX_FRAME as u64);
    assert_eq!(frame_bounds(&buf), Ok(None));
}

#[test]
fn tick_iteration_yields_the_run() {
    let mut run = [0u8; 128];
    let rn = fx_run(&mut run);
    let mut buf = [0u8; 256];
    let n = encode_tick(&mut buf, FX_TICK, FX_SEQ, 2, &run[..rn]);
    let (_, at, plen, _) = frame_bounds(&buf[..n]).unwrap().unwrap();
    let tick = parse_tick(&buf[at..at + plen]).unwrap();
    let recs: Vec<_> = tick.records().collect();
    assert_eq!(recs.len(), 2);
    assert_eq!(recs[0].intent, FX_INTENT);
    assert_eq!(recs[0].actor, FX_ACTOR);
    assert_eq!(recs[0].payload, &[0xDE, 0xAD, 0xBE, 0xEF]);
    assert_eq!(recs[1].intent, SYSTEM_INTENT);
    assert_eq!(recs[1].kind, FX_SYS_KIND);
    // The SimEvent slice is the record minus its envelope — the exact
    // bytes an apply receives.
    assert_eq!(recs[0].sim_event.len(), SIM_EVENT_HEADER + 4);
    assert_eq!(&recs[0].sim_event[..2], &FX_KIND.to_le_bytes());
}
