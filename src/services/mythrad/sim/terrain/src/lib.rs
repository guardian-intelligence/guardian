//! The terrain artifact: durable park geography as a versioned byte blob,
//! and the traversal semantics over it. A park's terrain is generated once
//! at park creation, stored beside the journal, and served content-
//! addressed; the sim never regenerates it, so procgen code can evolve
//! without silently rewriting live parks. Terrain changes reach a running
//! park only as an explicit terrain_set journal event.
//!
//! Blob layout (little-endian; raw bytes, transport compression is the
//! host's concern):
//!   header (16 B): magic "MYT1" u32, schema u32, w u16, h u16, reserved u32
//!   planes, each w*h cells in row-major order:
//!     ground   u8   behavioral class per cell: BLOCK | WALK | SWIM | STAIRS
//!     elev     u8   ground elevation (for SWIM, the water surface level)
//!     deck     u8   0 = no deck, else a bridge deck at elevation (value-1)
//!     obstacle u16  4x4 subtile blockers within the cell (bit = sy*4+sx)
//!     variant  u8   presentation only: tile art variation, never consulted
//!                   by any rule in this crate
//!
//! A cell carries up to two traversable surfaces — the ground and an
//! optional deck above it — so a dog can swim under the bridge another dog
//! is crossing. A nav position is therefore a Node: cell index plus a
//! surface bit.
//!
//! The blob's identity is mix64(fnv1a(bytes)), computable on every surface
//! including this no_std crate; it names the artifact at /terrain/<16-hex>
//! and folds into the world hash.
#![cfg_attr(not(test), no_std)]

pub const SCHEMA: u32 = 1;
pub const MAGIC: u32 = u32::from_le_bytes(*b"MYT1");
pub const HEADER: usize = 16;
/// Hard cap on cells: keeps nav scratch and node encoding (14 bits of cell
/// index) statically sized. Raising it is a compile-time constant bump plus
/// a module refresh.
pub const MAX_CELLS: usize = 128 * 128;
pub const BYTES_PER_CELL: usize = 6;
pub const MAX_BLOB: usize = HEADER + MAX_CELLS * BYTES_PER_CELL;
/// Subtiles per cell axis (the obstacle mask resolution).
pub const SUB: i32 = 4;

pub const BLOCK: u8 = 0;
pub const WALK: u8 = 1;
pub const SWIM: u8 = 2;
pub const STAIRS: u8 = 3;

pub const ERR_BLOB_BOUNDS: u32 = 1;
pub const ERR_BLOB_MAGIC: u32 = 2;
pub const ERR_BLOB_SCHEMA: u32 = 3;
pub const ERR_BLOB_DIMS: u32 = 4;
pub const ERR_BLOB_CLASS: u32 = 5;
pub const ERR_BLOB_EMPTY: u32 = 6;

/// A traversable position: 14 bits of cell index plus the deck bit.
/// `NONE` is the absent value (no target, no path).
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub struct Node(pub u16);

pub const DECK_BIT: u16 = 1 << 14;
pub const NONE: Node = Node(0xFFFF);

impl Node {
    pub fn ground(idx: u16) -> Node {
        Node(idx)
    }
    pub fn deck(idx: u16) -> Node {
        Node(idx | DECK_BIT)
    }
    pub fn at(idx: u16, on_deck: bool) -> Node {
        if on_deck { Node::deck(idx) } else { Node::ground(idx) }
    }
    pub fn idx(self) -> u16 {
        self.0 & (DECK_BIT - 1)
    }
    pub fn on_deck(self) -> bool {
        self.0 & DECK_BIT != 0
    }
    /// Whether the encoding itself is well-formed: no bits above the deck
    /// bit. Wire-supplied nodes (move_to payloads, snapshot fields) that
    /// fail this must be rejected — an aliased encoding would compare
    /// unequal to its canonical twin while resolving to the same cell,
    /// and every downstream identity check would silently fork.
    pub fn canonical(self) -> bool {
        self == NONE || self.0 & !(DECK_BIT | (DECK_BIT - 1)) == 0
    }
    /// Dense index for nav scratch arrays: [0, 2*MAX_CELLS).
    pub fn dense(self) -> usize {
        (self.0 & 0x7FFF) as usize
    }
}

/// A zero-copy view over a validated terrain blob: plane slices plus the
/// blob's identity. The referenced bytes are the single storage; nothing is
/// decoded into a second representation.
#[derive(Clone, Copy, Debug)]
pub struct Terrain<'a> {
    pub w: u16,
    pub h: u16,
    pub id: u64,
    pub schema: u32,
    ground: &'a [u8],
    elev: &'a [u8],
    deck: &'a [u8],
    obstacle: &'a [u8],
    variant: &'a [u8],
}

impl<'a> Terrain<'a> {
    /// Validates and wraps a blob. Structural checks only — the semantics
    /// of a hostile-but-well-formed blob are safe by construction (every
    /// query below is bounds-checked against w*h).
    pub fn parse(blob: &'a [u8]) -> Result<Terrain<'a>, u32> {
        if blob.len() < HEADER {
            return Err(ERR_BLOB_BOUNDS);
        }
        let rd32 =
            |at: usize| u32::from_le_bytes([blob[at], blob[at + 1], blob[at + 2], blob[at + 3]]);
        let rd16 = |at: usize| u16::from_le_bytes([blob[at], blob[at + 1]]);
        if rd32(0) != MAGIC {
            return Err(ERR_BLOB_MAGIC);
        }
        if rd32(4) != SCHEMA {
            return Err(ERR_BLOB_SCHEMA);
        }
        let (w, h) = (rd16(8), rd16(10));
        let cells = w as usize * h as usize;
        if w == 0 || h == 0 || cells > MAX_CELLS {
            return Err(ERR_BLOB_DIMS);
        }
        if blob.len() != HEADER + cells * BYTES_PER_CELL {
            return Err(ERR_BLOB_BOUNDS);
        }
        let ground = &blob[HEADER..HEADER + cells];
        if ground.iter().any(|&c| c > STAIRS) {
            return Err(ERR_BLOB_CLASS);
        }
        // At least one standable ground cell, so nearest_ground is total:
        // spawns, strandings, and relocations always have somewhere to go.
        if ground.iter().all(|&c| c == BLOCK) {
            return Err(ERR_BLOB_EMPTY);
        }
        Ok(Terrain {
            w,
            h,
            id: blob_id(blob),
            schema: SCHEMA,
            ground,
            elev: &blob[HEADER + cells..HEADER + 2 * cells],
            deck: &blob[HEADER + 2 * cells..HEADER + 3 * cells],
            obstacle: &blob[HEADER + 3 * cells..HEADER + 5 * cells],
            variant: &blob[HEADER + 5 * cells..HEADER + 6 * cells],
        })
    }

    pub fn cells(&self) -> usize {
        self.w as usize * self.h as usize
    }

    pub fn in_bounds(&self, x: i32, y: i32) -> bool {
        x >= 0 && y >= 0 && x < self.w as i32 && y < self.h as i32
    }

    pub fn idx(&self, x: i32, y: i32) -> u16 {
        (y as u16) * self.w + x as u16
    }

    pub fn xy(&self, idx: u16) -> (i32, i32) {
        ((idx % self.w) as i32, (idx / self.w) as i32)
    }

    pub fn class(&self, idx: u16) -> u8 {
        self.ground[idx as usize]
    }

    pub fn elev(&self, idx: u16) -> u8 {
        self.elev[idx as usize]
    }

    /// Deck elevation at a cell, if a deck exists there.
    pub fn deck_elev(&self, idx: u16) -> Option<u8> {
        match self.deck[idx as usize] {
            0 => None,
            d => Some(d - 1),
        }
    }

    pub fn variant(&self, idx: u16) -> u8 {
        self.variant[idx as usize]
    }

    /// Whether a node names an existing, standable surface. Non-canonical
    /// encodings never exist, so wire-supplied nodes are cleansed here for
    /// every consumer at once.
    pub fn exists(&self, n: Node) -> bool {
        if n == NONE || !n.canonical() || (n.idx() as usize) >= self.cells() {
            return false;
        }
        if n.on_deck() {
            self.deck[n.idx() as usize] != 0
        } else {
            self.class(n.idx()) != BLOCK
        }
    }

    /// The elevation a dog stands at on this node.
    pub fn node_elev(&self, n: Node) -> u8 {
        if n.on_deck() {
            self.deck[n.idx() as usize].saturating_sub(1)
        } else {
            self.elev(n.idx())
        }
    }

    /// THE traversal rule: can a dog standing on `from` cross into the
    /// orthogonally- or diagonally-adjacent cell as `to`? Every consumer —
    /// pathfinding edges, steering boundary checks, relocation — asks this
    /// one function, so the semantics cannot fork.
    ///
    ///   ground->ground  same class (walk-walk, swim-swim): equal elevation.
    ///                   stairs involved, or shoreline (walk<->swim): the
    ///                   elevations may differ by exactly one.
    ///   deck->deck      equal deck elevation.
    ///   ground->deck    boarding at a deck end: deck at the ground's
    ///                   level, and never from water — a swimmer passes
    ///                   under a bridge, it doesn't haul out mid-span.
    ///   deck->ground    stepping off the end: dry ground at the deck's
    ///                   level, or stairs within one.
    ///
    /// Adjacent SWIM cells at different levels are a waterfall face and
    /// fail the same-class rule — not traversable, by construction.
    pub fn can_cross(&self, from: Node, to: Node) -> bool {
        if !self.exists(from) || !self.exists(to) {
            return false;
        }
        let (fx, fy) = self.xy(from.idx());
        let (tx, ty) = self.xy(to.idx());
        if (fx - tx).abs() > 1 || (fy - ty).abs() > 1 || from.idx() == to.idx() {
            return false;
        }
        let (ea, eb) = (self.node_elev(from), self.node_elev(to));
        let diff = ea.abs_diff(eb);
        match (from.on_deck(), to.on_deck()) {
            (false, false) => {
                let (ca, cb) = (self.class(from.idx()), self.class(to.idx()));
                if ca == cb && ca != STAIRS {
                    diff == 0
                } else {
                    diff <= 1
                }
            }
            (true, true) => diff == 0,
            (false, true) => self.class(from.idx()) != SWIM && diff == 0,
            (true, false) => match self.class(to.idx()) {
                STAIRS => diff <= 1,
                SWIM => false,
                _ => diff == 0,
            },
        }
    }

    /// Whether the subtile at Q16.16 cell coordinates (x, y) is blocked by
    /// a small-prop obstacle. Only the ground surface has obstacles; a deck
    /// is a clean span.
    pub fn subtile_blocked(&self, x_q16: i32, y_q16: i32) -> bool {
        let cx = x_q16 >> 16;
        let cy = y_q16 >> 16;
        if !self.in_bounds(cx, cy) {
            return true;
        }
        let sx = (x_q16 >> 14) & 3; // (frac * 4) >> 16
        let sy = (y_q16 >> 14) & 3;
        let at = self.idx(cx, cy) as usize * 2;
        let mask = u16::from_le_bytes([self.obstacle[at], self.obstacle[at + 1]]);
        mask & (1 << (sy * SUB + sx)) != 0
    }

    /// Deterministic nearest standable ground node to a cell: rings of
    /// increasing Chebyshev radius, scanned in (dy, dx) order. Used to
    /// relocate dogs stranded by a terrain change and as the join-spawn
    /// fallback. Returns NONE only for a park with no standable ground.
    pub fn nearest_ground(&self, from_idx: u16) -> Node {
        let (cx, cy) = self.xy(from_idx % (self.cells() as u16).max(1));
        let max_r = (self.w.max(self.h)) as i32;
        for r in 0..=max_r {
            for dy in -r..=r {
                for dx in -r..=r {
                    if dx.abs().max(dy.abs()) != r {
                        continue;
                    }
                    let (x, y) = (cx + dx, cy + dy);
                    if !self.in_bounds(x, y) {
                        continue;
                    }
                    let idx = self.idx(x, y);
                    if self.class(idx) != BLOCK {
                        return Node::ground(idx);
                    }
                }
            }
        }
        NONE
    }
}

/// The blob's identity: mix64 over FNV-1a of every byte. Single name for
/// the artifact everywhere — journal payloads, the /terrain URL, the world
/// hash — and computable by this crate on any surface.
pub fn blob_id(blob: &[u8]) -> u64 {
    mythra_sim_core::mix64(mythra_sim_core::fnv1a(blob))
}

/// Writes a terrain blob into a caller-provided buffer (no allocation, so
/// the same builder serves no_std tests and the std fixture generator).
/// Planes default to WALK at elevation 0, no decks, no obstacles.
pub struct Builder<'a> {
    buf: &'a mut [u8],
    w: u16,
    h: u16,
}

impl<'a> Builder<'a> {
    /// Panics if the buffer is too small or the dims exceed MAX_CELLS —
    /// builder misuse is an authoring bug, not a runtime input.
    pub fn new(buf: &'a mut [u8], w: u16, h: u16) -> Builder<'a> {
        let cells = w as usize * h as usize;
        assert!(w > 0 && h > 0 && cells <= MAX_CELLS);
        let len = HEADER + cells * BYTES_PER_CELL;
        assert!(buf.len() >= len);
        buf[..len].fill(0);
        buf[0..4].copy_from_slice(&MAGIC.to_le_bytes());
        buf[4..8].copy_from_slice(&SCHEMA.to_le_bytes());
        buf[8..10].copy_from_slice(&w.to_le_bytes());
        buf[10..12].copy_from_slice(&h.to_le_bytes());
        let mut b = Builder { buf, w, h };
        for y in 0..h as i32 {
            for x in 0..w as i32 {
                b.set_ground(x, y, WALK, 0);
            }
        }
        b
    }

    fn at(&self, plane: usize, x: i32, y: i32) -> usize {
        let cells = self.w as usize * self.h as usize;
        HEADER + plane * cells + (y as usize * self.w as usize + x as usize)
    }

    pub fn set_ground(&mut self, x: i32, y: i32, class: u8, elev: u8) {
        let (g, e) = (self.at(0, x, y), self.at(1, x, y));
        self.buf[g] = class;
        self.buf[e] = elev;
    }

    pub fn set_deck(&mut self, x: i32, y: i32, deck_elev: u8) {
        let d = self.at(2, x, y);
        self.buf[d] = deck_elev + 1;
    }

    /// Sets the 4x4 obstacle mask for a cell (bit = sy*4+sx).
    pub fn set_obstacle(&mut self, x: i32, y: i32, mask: u16) {
        let cells = self.w as usize * self.h as usize;
        let o = HEADER + 3 * cells + 2 * (y as usize * self.w as usize + x as usize);
        self.buf[o..o + 2].copy_from_slice(&mask.to_le_bytes());
    }

    pub fn set_variant(&mut self, x: i32, y: i32, v: u8) {
        let at = self.at(5, x, y);
        self.buf[at] = v;
    }

    pub fn finish(self) -> &'a [u8] {
        let len = HEADER + self.w as usize * self.h as usize * BYTES_PER_CELL;
        &self.buf[..len]
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn tiny() -> Vec<u8> {
        // 8x8: grass at elev 0, a raised 2-wide plateau at x>=6 (elev 1)
        // with stairs at (5,3), a water column at x=2 (elev 0), and a deck
        // at elev 1 spanning (2..=3, 6) boarded from the plateau... kept
        // minimal: each rule gets its own dedicated cells instead.
        let mut buf = vec![0u8; HEADER + 64 * BYTES_PER_CELL];
        let mut b = Builder::new(&mut buf, 8, 8);
        for y in 0..8 {
            b.set_ground(2, y, SWIM, 0); // river column
            b.set_ground(6, y, WALK, 1); // plateau column
            b.set_ground(7, y, WALK, 1);
        }
        b.set_ground(5, 3, STAIRS, 0); // stairs up to the plateau
        b.set_ground(4, 0, BLOCK, 0); // a wall cell
        b.set_deck(2, 6, 1); // deck over the river
        b.set_deck(1, 6, 1); // deck end over the west bank (bank is elev 0)
        b.set_deck(2, 3, 0); // a deck lying at water level
        b.set_obstacle(4, 4, 0b0000_0110_0110_0000); // barrel: center 2x2
        b.finish().to_vec()
    }

    fn t(blob: &[u8]) -> Terrain<'_> {
        Terrain::parse(blob).unwrap()
    }

    #[test]
    fn parse_validates_structure() {
        let blob = tiny();
        let terr = t(&blob);
        assert_eq!((terr.w, terr.h), (8, 8));
        assert_eq!(terr.id, blob_id(&blob));

        assert_eq!(Terrain::parse(&blob[..10]).unwrap_err(), ERR_BLOB_BOUNDS);
        let mut bad = blob.clone();
        bad[0] = b'X';
        assert_eq!(Terrain::parse(&bad).unwrap_err(), ERR_BLOB_MAGIC);
        let mut bad = blob.clone();
        bad[4] = 9;
        assert_eq!(Terrain::parse(&bad).unwrap_err(), ERR_BLOB_SCHEMA);
        let mut bad = blob.clone();
        bad[HEADER] = 200; // unknown ground class
        assert_eq!(Terrain::parse(&bad).unwrap_err(), ERR_BLOB_CLASS);
        let mut bad = blob.clone();
        bad.pop();
        assert_eq!(Terrain::parse(&bad).unwrap_err(), ERR_BLOB_BOUNDS);
        // a park with nowhere to stand can never host a dog
        let mut empty = vec![0u8; HEADER + 16 * BYTES_PER_CELL];
        let mut b = Builder::new(&mut empty, 4, 4);
        for y in 0..4 {
            for x in 0..4 {
                b.set_ground(x, y, BLOCK, 0);
            }
        }
        let empty = b.finish().to_vec();
        assert_eq!(Terrain::parse(&empty).unwrap_err(), ERR_BLOB_EMPTY);
    }

    #[test]
    fn aliased_node_encodings_never_exist() {
        let blob = tiny();
        let terr = t(&blob);
        let idx = terr.idx(0, 0); // standable ground
        assert!(terr.exists(Node::ground(idx)));
        assert!(!terr.exists(Node(idx | 0x8000)));
        assert!(!terr.exists(Node(idx | 0x8000 | DECK_BIT)));
        assert!(!Node(idx | 0x8000).canonical());
        assert!(Node::deck(idx).canonical());
        assert!(NONE.canonical());
    }

    #[test]
    fn identity_is_content_sensitive() {
        let blob = tiny();
        let mut other = blob.clone();
        let variant_plane = HEADER + 5 * 64;
        other[variant_plane] ^= 1; // presentation byte still changes identity
        assert_ne!(blob_id(&blob), blob_id(&other));
    }

    // The traversal truth table: one assertion per rule in can_cross.
    #[test]
    fn crossing_rules() {
        let blob = tiny();
        let terr = t(&blob);
        let g = |x: i32, y: i32| Node::ground(terr.idx(x, y));
        let d = |x: i32, y: i32| Node::deck(terr.idx(x, y));

        // walk->walk flat
        assert!(terr.can_cross(g(0, 0), g(1, 0)));
        // walk->walk across an elevation cliff: no
        assert!(!terr.can_cross(g(5, 0), g(6, 0)));
        // stairs bridge one level, both directions
        assert!(terr.can_cross(g(4, 3), g(5, 3)));
        assert!(terr.can_cross(g(5, 3), g(6, 3)));
        assert!(terr.can_cross(g(6, 3), g(5, 3)));
        // shoreline: walk into water and back out
        assert!(terr.can_cross(g(1, 1), g(2, 1)));
        assert!(terr.can_cross(g(2, 1), g(1, 1)));
        // swim->swim flat along the river
        assert!(terr.can_cross(g(2, 1), g(2, 2)));
        // nothing crosses into a wall, nothing leaves one
        assert!(!terr.can_cross(g(3, 0), g(4, 0)));
        assert!(!terr.can_cross(g(4, 0), g(3, 0)));
        // deck->deck flat
        assert!(terr.can_cross(d(1, 6), d(2, 6)));
        // deck->ground only at matching level (west bank is elev 0, deck 1)
        assert!(!terr.can_cross(d(1, 6), g(0, 6)));
        // ground under the deck is open water: the two surfaces coexist
        assert!(terr.can_cross(g(2, 5), g(2, 6)));
        // boarding a deck from ground needs the levels to meet
        assert!(!terr.can_cross(g(1, 5), d(1, 6)));
        // water never boards a deck (even at matching level), and a deck
        // never steps off into water
        assert!(!terr.can_cross(g(2, 2), d(2, 3)));
        assert!(!terr.can_cross(d(2, 3), g(2, 2)));
        // self and non-adjacent are never crossings
        assert!(!terr.can_cross(g(0, 0), g(0, 0)));
        assert!(!terr.can_cross(g(0, 0), g(3, 0)));
    }

    #[test]
    fn waterfall_face_is_impassable() {
        let mut buf = vec![0u8; HEADER + 16 * BYTES_PER_CELL];
        let mut b = Builder::new(&mut buf, 4, 4);
        b.set_ground(1, 0, SWIM, 1);
        b.set_ground(1, 1, SWIM, 0);
        let blob = b.finish().to_vec();
        let terr = t(&blob);
        let g = |x: i32, y: i32| Node::ground(terr.idx(x, y));
        assert!(!terr.can_cross(g(1, 0), g(1, 1)));
        assert!(!terr.can_cross(g(1, 1), g(1, 0)));
    }

    #[test]
    fn subtile_obstacles_resolve_at_quarter_cell() {
        let blob = tiny();
        let terr = t(&blob);
        let q = |cx: i32, sub_x: i32, cy: i32, sub_y: i32| {
            (
                (cx << 16) + (sub_x << 14) + (1 << 13),
                (cy << 16) + (sub_y << 14) + (1 << 13),
            )
        };
        // barrel occupies the center 2x2 of cell (4,4)
        let (x, y) = q(4, 1, 4, 1);
        assert!(terr.subtile_blocked(x, y));
        let (x, y) = q(4, 0, 4, 0);
        assert!(!terr.subtile_blocked(x, y));
        let (x, y) = q(4, 3, 4, 3);
        assert!(!terr.subtile_blocked(x, y));
        // out of bounds is always blocked
        assert!(terr.subtile_blocked(-1, 0));
        assert!(terr.subtile_blocked(0, 9 << 16));
    }

    #[test]
    fn nearest_ground_is_deterministic_and_escapes_walls() {
        let blob = tiny();
        let terr = t(&blob);
        // from the wall cell, the scan finds the same neighbor every time
        let wall = terr.idx(4, 0);
        let a = terr.nearest_ground(wall);
        assert_eq!(a, terr.nearest_ground(wall));
        assert!(terr.exists(a));
        assert!(!a.on_deck());
        // from a standable cell, the answer is the cell itself
        let open = terr.idx(0, 0);
        assert_eq!(terr.nearest_ground(open), Node::ground(open));
    }

    #[test]
    fn node_encoding_round_trips() {
        let n = Node::deck(1234);
        assert!(n.on_deck());
        assert_eq!(n.idx(), 1234);
        assert_eq!(Node::ground(1234).dense() + MAX_CELLS, n.dense());
        assert_ne!(NONE, Node::ground(0));
    }
}
