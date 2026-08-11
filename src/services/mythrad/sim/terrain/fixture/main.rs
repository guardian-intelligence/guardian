//! Emits the fixture park: the hand-authored 128x128 terrain every park is
//! born with until procedural generation exists. The output is a pure
//! function of this source, built by Bazel and committed (diff-tested), so
//! the blob's identity — and therefore every park's world hash — can only
//! change through an explicit refresh.
//!
//! The layout exercises every traversal rule: a river with a waterfall
//! line, two decked bridges on raised platform approaches, a two-level
//! plateau, a pond, scattered sub-tile obstacles, and a fenced border.

use mythra_sim_terrain::{BLOCK, BYTES_PER_CELL, Builder, HEADER, STAIRS, SWIM, WALK};

const W: u16 = 128;
const H: u16 = 128;

fn h32(x: u32, y: u32) -> u32 {
    let mut v = x.wrapping_mul(0x9E37_79B9) ^ y.wrapping_mul(0x85EB_CA6B);
    v ^= v >> 15;
    v = v.wrapping_mul(0xC2B2_AE35);
    v ^ (v >> 13)
}

fn main() {
    let out = std::env::args().nth(1).expect("usage: fixture <out-path>");
    let mut buf = vec![0u8; HEADER + W as usize * H as usize * BYTES_PER_CELL];
    let mut b = Builder::new(&mut buf, W, H);

    // grass variant noise (presentation only)
    for y in 0..H as i32 {
        for x in 0..W as i32 {
            b.set_variant(x, y, (h32(x as u32, y as u32) % 4) as u8);
        }
    }

    // the river: nine cells wide, elev 1 upstream of the waterfall at y=64
    for y in 0..H as i32 {
        for x in 58..=66 {
            b.set_ground(x, y, SWIM, if y < 64 { 1 } else { 0 });
        }
    }

    // upstream banks rise with the water so the shoreline rule holds
    for y in 0..64 {
        for x in [56, 57, 67, 68] {
            b.set_ground(x, y, WALK, 1);
        }
    }
    // bank ramps down to the lowlands beside the waterfall line
    for x in [56, 57, 67, 68] {
        b.set_ground(x, 64, STAIRS, 0);
    }

    // two bridges: platforms at deck level, stairs at both ends
    for &y in &[40, 90] {
        let deck_elev = if y < 64 { 2 } else { 1 };
        for x in 54..=57 {
            b.set_ground(x, y, WALK, deck_elev);
        }
        for x in 67..=70 {
            b.set_ground(x, y, WALK, deck_elev);
        }
        b.set_ground(53, y, STAIRS, deck_elev - 1);
        b.set_ground(71, y, STAIRS, deck_elev - 1);
        for x in 58..=66 {
            b.set_deck(x, y, deck_elev);
        }
    }

    // the plateau: elev 1 with an elev 2 mesa, stairs on the west edges
    for y in 20..=44 {
        for x in 96..=120 {
            b.set_ground(x, y, WALK, 1);
        }
    }
    for y in 28..=36 {
        b.set_ground(95, y, STAIRS, 0);
    }
    for y in 26..=38 {
        for x in 104..=112 {
            b.set_ground(x, y, WALK, 2);
        }
    }
    for y in 30..=34 {
        b.set_ground(103, y, STAIRS, 1);
    }

    // the pond
    for y in 88..=104 {
        for x in 16..=32 {
            let (dx, dy) = (x - 24, y - 96);
            if dx * dx + dy * dy <= 64 {
                b.set_ground(x, y, SWIM, 0);
            }
        }
    }

    // fenced border
    for y in 0..H as i32 {
        for x in 0..W as i32 {
            if x == 0 || y == 0 || x == W as i32 - 1 || y == H as i32 - 1 {
                b.set_ground(x, y, BLOCK, 0);
            }
        }
    }

    // a dirt path (presentation) from the west gate over bridge A
    for x in 4..=57 {
        b.set_variant(x, 40, 8);
        b.set_variant(x, 41, 8);
    }
    for x in 67..=95 {
        b.set_variant(x, 40, 8);
    }
    for y in 32..=40 {
        b.set_variant(95, y, 8);
    }

    // scattered barrels and rocks: sparse, on flat open grass only
    let blob_probe = b.finish();
    let mut buf2 = blob_probe.to_vec();
    let cells = W as usize * H as usize;
    for y in 2..H as i32 - 2 {
        for x in 2..W as i32 - 2 {
            if h32(x as u32 ^ 0xB0BA, y as u32) % 149 != 0 {
                continue;
            }
            let at = HEADER + (y as usize * W as usize + x as usize);
            let (class, elev) = (buf2[at], buf2[at + cells]);
            let decked = buf2[at + 2 * cells] != 0;
            if class != WALK || elev != 0 || decked {
                continue;
            }
            let mask: u16 = match h32(x as u32, y as u32 ^ 0xCAFE) % 3 {
                0 => 0b0000_0110_0110_0000, // barrel: center 2x2
                1 => 0b0000_0000_0110_0000, // small rock
                _ => 0b0000_0110_0110_0110, // crate stack
            };
            let ob = HEADER + 3 * cells + 2 * (y as usize * W as usize + x as usize);
            buf2[ob..ob + 2].copy_from_slice(&mask.to_le_bytes());
        }
    }

    mythra_sim_terrain::Terrain::parse(&buf2).expect("fixture must parse");
    std::fs::write(&out, &buf2).expect("write fixture blob");
}
