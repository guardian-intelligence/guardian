//! Movement over terrain: deterministic A* on the (cell, surface) node
//! graph, and the fixed-point steering that walks a dog along the result.
//! Both are pure functions of (terrain, inputs) — no hidden state, no
//! iteration-order dependence — so a replica that holds the same state
//! computes the same movement forever.
//!
//! The sim stores no paths. A dog's movement state is exactly
//! (position, waypoint, target): the waypoint is recomputed by A* only at
//! state transitions (a new target, a corner reached, a blocked step), so
//! any snapshot restores to bit-identical movement by construction — there
//! is no derived path cache whose absence could diverge a replay.
//!
//! Determinism inventory: neighbor expansion order is a fixed array; the
//! open list is a binary heap over packed (f, node) integers, so ties break
//! by node index; costs are integers; the search aborts (deterministically)
//! at a cost cap instead of overflowing.
#![cfg_attr(not(test), no_std)]

use mythra_sim_core::{ONE, isqrt_q16};
use mythra_sim_terrain::{MAX_CELLS, NONE, Node, SWIM, Terrain};

const NODES: usize = 2 * MAX_CELLS;
/// Lazy-deletion heap: nodes can be pushed more than once, ~1.5x nodes is
/// generous for grid graphs. Overflow fails the search — deterministically.
const HEAP_CAP: usize = NODES + NODES / 2;
/// Searches abort past this path cost (u16 f-packing headroom); a real
/// path across a 128x128 park costs well under a quarter of it.
const COST_CAP: u32 = 60_000;

const ORTH_COST: u32 = 10;
const DIAG_COST: u32 = 14;
/// Entering water costs triple: dogs paddle, and paths prefer dry land
/// unless swimming genuinely wins.
const SWIM_FACTOR: u32 = 3;

/// Fixed expansion order; orthogonals first. Part of the determinism
/// contract — reordering this array changes tie-broken paths everywhere.
const DIRS: [(i32, i32); 8] = [
    (1, 0),
    (-1, 0),
    (0, 1),
    (0, -1),
    (1, 1),
    (1, -1),
    (-1, 1),
    (-1, -1),
];

/// Reusable search memory, allocation-free after creation. Generation
/// stamps make every search start from clean arrays without clearing them.
pub struct Scratch {
    g: [u32; NODES],
    parent: [u16; NODES],
    mark: [u16; NODES],
    gen_id: u16,
    heap: [u32; HEAP_CAP],
}

impl Scratch {
    pub const NEW: Scratch = Scratch {
        g: [0; NODES],
        parent: [0; NODES],
        mark: [0; NODES],
        gen_id: 0,
        heap: [0; HEAP_CAP],
    };

    fn begin(&mut self) {
        self.gen_id = self.gen_id.wrapping_add(1);
        if self.gen_id == 0 {
            self.mark = [0; NODES];
            self.gen_id = 1;
        }
    }
}

fn dense_node(dense: usize) -> Node {
    if dense >= MAX_CELLS {
        Node::deck((dense - MAX_CELLS) as u16)
    } else {
        Node::ground(dense as u16)
    }
}

/// Octile-distance heuristic in the 10/14 cost scale. Ignores surfaces and
/// swim penalties, so it never overestimates.
fn heuristic(t: &Terrain, a: Node, b: Node) -> u32 {
    let (ax, ay) = t.xy(a.idx());
    let (bx, by) = t.xy(b.idx());
    let dx = (ax - bx).unsigned_abs();
    let dy = (ay - by).unsigned_abs();
    let (lo, hi) = if dx < dy { (dx, dy) } else { (dy, dx) };
    ORTH_COST * hi + (DIAG_COST - ORTH_COST) * lo
}

fn enter_cost(t: &Terrain, to: Node, diagonal: bool) -> u32 {
    let base = if diagonal { DIAG_COST } else { ORTH_COST };
    if !to.on_deck() && t.class(to.idx()) == SWIM {
        base * SWIM_FACTOR
    } else {
        base
    }
}

struct Heap<'a> {
    buf: &'a mut [u32; HEAP_CAP],
    len: usize,
}

impl Heap<'_> {
    /// Entry packing: f in the high 16 bits, dense node in the low 16 —
    /// so the min-heap order is (f, node index), and equal-cost frontiers
    /// expand in a platform-free total order.
    fn push(&mut self, f: u32, dense: usize) -> bool {
        if self.len == HEAP_CAP {
            return false;
        }
        let entry = (f << 16) | dense as u32;
        let mut i = self.len;
        self.len += 1;
        while i > 0 {
            let p = (i - 1) / 2;
            if self.buf[p] <= entry {
                break;
            }
            self.buf[i] = self.buf[p];
            i = p;
        }
        self.buf[i] = entry;
        true
    }

    fn pop(&mut self) -> Option<(u32, usize)> {
        if self.len == 0 {
            return None;
        }
        let top = self.buf[0];
        self.len -= 1;
        if self.len > 0 {
            let last = self.buf[self.len];
            let mut i = 0;
            loop {
                let (l, r) = (2 * i + 1, 2 * i + 2);
                if l >= self.len {
                    break;
                }
                let c = if r < self.len && self.buf[r] < self.buf[l] {
                    r
                } else {
                    l
                };
                if last <= self.buf[c] {
                    break;
                }
                self.buf[i] = self.buf[c];
                i = c;
            }
            self.buf[i] = last;
        }
        Some(((top >> 16), (top & 0xFFFF) as usize))
    }
}

/// Expands `from`'s neighbors in DIRS order into `visit(to, step_cost)`.
/// Orthogonal steps may change surface (boarding or leaving a deck);
/// diagonal steps keep the surface and require both flanking orthogonal
/// crossings, so paths never cut corners the steering couldn't walk.
fn for_each_edge(t: &Terrain, from: Node, mut visit: impl FnMut(Node, u32)) {
    let (x, y) = t.xy(from.idx());
    for (i, &(dx, dy)) in DIRS.iter().enumerate() {
        let (nx, ny) = (x + dx, y + dy);
        if !t.in_bounds(nx, ny) {
            continue;
        }
        let nidx = t.idx(nx, ny);
        let diagonal = i >= 4;
        if diagonal {
            let same = |idx| Node::at(idx, from.on_deck());
            let to = same(nidx);
            let flank_a = t.in_bounds(x + dx, y) && t.can_cross(from, same(t.idx(x + dx, y)));
            let flank_b = t.in_bounds(x, y + dy) && t.can_cross(from, same(t.idx(x, y + dy)));
            if flank_a && flank_b && t.can_cross(from, to) {
                visit(to, enter_cost(t, to, true));
            }
        } else {
            for to in [Node::ground(nidx), Node::deck(nidx)] {
                if t.can_cross(from, to) {
                    visit(to, enter_cost(t, to, false));
                }
            }
        }
    }
}

/// Runs A* and leaves the parent chain in scratch. Returns whether `to`
/// was reached.
fn search(t: &Terrain, s: &mut Scratch, from: Node, to: Node) -> bool {
    // Compare dense indices, not raw encodings: the goal test below is
    // dense-based, so an equality mismatch here would let a same-node pair
    // "succeed" with an empty parent chain.
    if !t.exists(from) || !t.exists(to) || from.dense() == to.dense() {
        return false;
    }
    s.begin();
    let fd = from.dense();
    s.mark[fd] = s.gen_id;
    s.g[fd] = 0;
    s.parent[fd] = NONE.0;
    let mut heap = Heap {
        buf: &mut s.heap,
        len: 0,
    };
    heap.push(heuristic(t, from, to), fd);
    while let Some((ef, dense)) = heap.pop() {
        let node = dense_node(dense);
        let g = s.g[dense];
        if ef != g + heuristic(t, node, to) {
            continue; // stale lazy-deletion entry
        }
        if dense == to.dense() {
            return true;
        }
        let gen_id = s.gen_id;
        let mut overflow = false;
        // Split-borrow the scratch fields so the closure can write them
        // while the heap holds `s.heap`.
        let (g_arr, parent, mark) = (&mut s.g, &mut s.parent, &mut s.mark);
        for_each_edge(t, node, |nb, cost| {
            let nd = nb.dense();
            let ng = g + cost;
            if ng > COST_CAP {
                return;
            }
            if mark[nd] == gen_id && g_arr[nd] <= ng {
                return;
            }
            mark[nd] = gen_id;
            g_arr[nd] = ng;
            parent[nd] = node.0;
            if !heap.push(ng + heuristic(t, nb, to), nd) {
                overflow = true;
            }
        });
        if overflow {
            return false;
        }
    }
    false
}

/// The next waypoint on the optimal path from `from` to `to`: the last
/// node of the first straight, same-surface run — the point steering can
/// walk to in a straight line. Returns `to` itself when the whole path is
/// one straight run, NONE when unreachable.
pub fn first_waypoint(t: &Terrain, s: &mut Scratch, from: Node, to: Node) -> Node {
    if !search(t, s, from, to) {
        return NONE;
    }
    // Reverse the parent chain in place (scratch is ours to destroy), then
    // walk forward from `from`.
    let mut cur = to.0;
    let mut prev = NONE.0;
    while cur != from.0 {
        let next = s.parent[Node(cur).dense()];
        s.parent[Node(cur).dense()] = prev;
        prev = cur;
        cur = next;
    }
    s.parent[from.dense()] = prev;

    // A "straight run" is homogeneous in direction AND in both endpoint
    // surfaces: any boarding or exit ends the run, so every surface
    // transition happens exactly at a waypoint's own cell — the one place
    // steering switches surface.
    let step = |a: Node, b: Node| {
        let (ax, ay) = t.xy(a.idx());
        let (bx, by) = t.xy(b.idx());
        (bx - ax, by - ay, a.on_deck(), b.on_deck())
    };
    let mut wp = Node(s.parent[from.dense()]);
    let dir = step(from, wp);
    loop {
        if wp == to {
            return wp;
        }
        let next = Node(s.parent[wp.dense()]);
        if next == NONE || step(wp, next) != dir {
            return wp;
        }
        wp = next;
    }
}

/// Path cost between two nodes under the exact search the waypoint system
/// runs (10/14 octile scale, swim penalty included); None when
/// unreachable. Lets callers rank destinations by what a dog would
/// actually walk, with the same determinism guarantees as the pathing.
pub fn path_cost(t: &Terrain, s: &mut Scratch, from: Node, to: Node) -> Option<u32> {
    if !t.exists(from) || !t.exists(to) {
        return None;
    }
    if from.dense() == to.dense() {
        return Some(0);
    }
    if search(t, s, from, to) {
        Some(s.g[to.dense()])
    } else {
        None
    }
}

pub const WALK_SPEED: i32 = ONE as i32 / 8; // 3 cells/s at 24Hz
pub const SWIM_SPEED: i32 = ONE as i32 / 16;
/// Within this Q16.16 distance of a waypoint's center, the waypoint is
/// reached. A quarter cell: close enough that the next segment's line
/// stays legal, far enough that arrival never oscillates.
const ARRIVE: i64 = ONE / 4;
const HALF: i32 = (ONE / 2) as i32;

/// Center of a cell in Q16.16 coordinates.
pub fn center(t: &Terrain, n: Node) -> (i32, i32) {
    let (x, y) = t.xy(n.idx());
    ((x << 16) + HALF, (y << 16) + HALF)
}

#[derive(PartialEq, Eq, Debug, Clone, Copy)]
pub enum Step {
    Moving,
    Arrived,
    Blocked,
}

#[derive(PartialEq, Eq, Clone, Copy)]
enum Axis {
    Moved,
    NoDelta,
    /// Rejected by a subtile obstacle: steer around it.
    Obstacle,
    /// Rejected by the terrain crossing rules: a wall the path should have
    /// avoided — never slide along it into un-pathed territory.
    Crossing,
}

/// One tick of steering toward a waypoint: capped-velocity advance,
/// decomposed per axis so each sub-move crosses at most one cell boundary
/// (speeds are far below one cell per tick; `boosted` doubles them and is
/// bounded by the same static assertion). Boundary crossings ask
/// can_cross — the same rule pathfinding used — and the surface flips
/// exactly when the crossing enters the waypoint's cell on the waypoint's
/// surface.
///
/// Obstacle response is stateless wall-following: when an axis is rejected
/// by a subtile obstacle, the cross axis steers toward the nearest free
/// subtile lane of the obstructing column (or row) instead of toward the
/// waypoint — otherwise the pull back toward the line re-blocks the axis
/// every tick and the dog jiggles at the obstacle face forever. The lane
/// choice is a pure function of position, so every replica rounds the
/// barrel the same way.
///
/// TODO: this pathfinding is demonstration-only, not final (user ruling
/// 2026-08-14). Known feel defect kept deliberately: arrival is tested
/// before moving, so a dog pauses one tick at every waypoint corner. A
/// real pathfinder replaces this wholesale; fixing the pause alone would
/// change trajectories and force a module epoch for a throwaway.
pub fn step_toward(
    t: &Terrain,
    x: &mut i32,
    y: &mut i32,
    on_deck: &mut bool,
    waypoint: Node,
    boosted: bool,
) -> Step {
    const _: () = assert!(
        2 * WALK_SPEED <= HALF,
        "a boosted axis sub-move must not span two boundaries"
    );
    let (tx, ty) = center(t, waypoint);
    let (dx, dy) = ((tx - *x) as i64, (ty - *y) as i64);
    let dist = isqrt_q16(dx * dx + dy * dy);
    if dist <= ARRIVE {
        return Step::Arrived;
    }
    let here = t.idx(*x >> 16, *y >> 16);
    let swimming = !*on_deck && t.class(here) == SWIM;
    let base = if swimming { SWIM_SPEED } else { WALK_SPEED } as i64;
    let speed = if boosted { 2 * base } else { base };
    let step = if dist < speed { dist } else { speed };
    let sx = (dx * step / dist) as i32;
    let sy = (dy * step / dist) as i32;

    let rx = try_axis(t, x, y, on_deck, waypoint, sx, true);
    if rx == Axis::Obstacle {
        // steer y toward the free lane of the column that just blocked x
        if let Some(d) = free_lane_dy(t, *x + sx, *y, step as i32) {
            if try_axis(t, x, y, on_deck, waypoint, d, false) == Axis::Moved {
                return Step::Moving;
            }
        }
    }
    let ry = try_axis(t, x, y, on_deck, waypoint, sy, false);
    if rx == Axis::Moved || ry == Axis::Moved {
        return Step::Moving;
    }
    if ry == Axis::Obstacle {
        if let Some(d) = free_lane_dx(t, *x, *y + sy, step as i32) {
            if try_axis(t, x, y, on_deck, waypoint, d, true) == Axis::Moved {
                return Step::Moving;
            }
        }
    }
    Step::Blocked
}

/// The y-delta that walks toward the nearest obstacle-free subtile row of
/// the cell column at (bx, y) — the column an x-move was just rejected by.
/// None when the whole column is blocked (treat as a wall).
fn free_lane_dy(t: &Terrain, bx: i32, y: i32, step: i32) -> Option<i32> {
    let cell_y = y & !0xFFFF;
    let cur_row = (y >> 14) & 3;
    let mut best: Option<(i32, i32)> = None; // (distance, target_y)
    for r in 0..4i32 {
        let lane_y = cell_y + (r << 14) + (1 << 13); // row center
        if t.subtile_blocked(bx, lane_y) {
            continue;
        }
        let d = (r - cur_row).abs();
        if best.is_none() || d < best.unwrap().0 {
            best = Some((d, lane_y));
        }
    }
    let (_, target) = best?;
    let delta = (target - y).clamp(-step, step);
    if delta == 0 { None } else { Some(delta) }
}

fn free_lane_dx(t: &Terrain, x: i32, by: i32, step: i32) -> Option<i32> {
    let cell_x = x & !0xFFFF;
    let cur_col = (x >> 14) & 3;
    let mut best: Option<(i32, i32)> = None;
    for c in 0..4i32 {
        let lane_x = cell_x + (c << 14) + (1 << 13);
        if t.subtile_blocked(lane_x, by) {
            continue;
        }
        let d = (c - cur_col).abs();
        if best.is_none() || d < best.unwrap().0 {
            best = Some((d, lane_x));
        }
    }
    let (_, target) = best?;
    let delta = (target - x).clamp(-step, step);
    if delta == 0 { None } else { Some(delta) }
}

fn try_axis(
    t: &Terrain,
    x: &mut i32,
    y: &mut i32,
    on_deck: &mut bool,
    waypoint: Node,
    delta: i32,
    is_x: bool,
) -> Axis {
    if delta == 0 {
        return Axis::NoDelta;
    }
    let (nx, ny) = if is_x {
        (*x + delta, *y)
    } else {
        (*x, *y + delta)
    };
    let (cur_cx, cur_cy) = (*x >> 16, *y >> 16);
    let (new_cx, new_cy) = (nx >> 16, ny >> 16);
    let mut deck_after = *on_deck;
    if (new_cx, new_cy) != (cur_cx, cur_cy) {
        if !t.in_bounds(new_cx, new_cy) {
            return Axis::Crossing;
        }
        let new_idx = t.idx(new_cx, new_cy);
        let cur = Node::at(t.idx(cur_cx, cur_cy), *on_deck);
        // Entering the waypoint's own cell adopts the waypoint's surface;
        // any other crossing keeps the current one.
        deck_after = if new_idx == waypoint.idx() {
            waypoint.on_deck()
        } else {
            *on_deck
        };
        if !t.can_cross(cur, Node::at(new_idx, deck_after)) {
            return Axis::Crossing;
        }
    }
    if !deck_after && t.subtile_blocked(nx, ny) {
        return Axis::Obstacle;
    }
    *x = nx;
    *y = ny;
    *on_deck = deck_after;
    Axis::Moved
}

#[cfg(test)]
mod tests {
    use super::*;
    use mythra_sim_terrain::{BLOCK, BYTES_PER_CELL, Builder, HEADER, STAIRS, WALK};
    use std::sync::Mutex;

    static SCRATCH: Mutex<Scratch> = Mutex::new(Scratch::NEW);

    // A 16x16 course exercising every edge type: a river with a waterfall,
    // a bridge on raised platforms, stairs, a plateau, and a barrel.
    //
    //   x: 0..5 grass | 6..8 river | 9..15 grass; plateau y 12..15 x 12..15
    //   waterfall at river y=8 (north half elev 1, south half elev 0)
    //   bridge deck elev 1 at y=4 spanning x 4..10, platforms+stairs at ends
    fn course() -> Vec<u8> {
        let mut buf = vec![0u8; HEADER + 256 * BYTES_PER_CELL];
        let mut b = Builder::new(&mut buf, 16, 16);
        for y in 0..16 {
            for x in 6..9 {
                b.set_ground(x, y, mythra_sim_terrain::SWIM, if y < 8 { 1 } else { 0 });
            }
        }
        // banks beside the north river sit at elev 0; swimmers can still
        // enter (shoreline rule) but not cross the waterfall face.
        // bridge: platforms at elev 1 on both banks, stairs up, deck across
        for x in [4, 5, 9, 10] {
            b.set_ground(x, 4, WALK, 1);
        }
        b.set_ground(3, 4, STAIRS, 0);
        b.set_ground(11, 4, STAIRS, 0);
        for x in 5..=9 {
            b.set_deck(x, 4, 1);
        }
        // plateau with stairs at (11,13)
        for y in 12..16 {
            for x in 12..16 {
                b.set_ground(x, y, WALK, 1);
            }
        }
        b.set_ground(11, 13, STAIRS, 0);
        // a wall segment and a barrel
        for y in 9..12 {
            b.set_ground(2, y, BLOCK, 0);
        }
        b.set_obstacle(2, 2, 0b0000_0110_0110_0000);
        b.finish().to_vec()
    }

    fn walk_straight(t: &Terrain, mut from: Node, to: Node) {
        // A waypoint promises a straight same-surface run; verify every
        // unit step of it is a legal crossing.
        let (fx, fy) = t.xy(from.idx());
        let (tx, ty) = t.xy(to.idx());
        let (dx, dy) = ((tx - fx).signum(), (ty - fy).signum());
        assert!(
            (tx - fx) == 0 || (ty - fy) == 0 || (tx - fx).abs() == (ty - fy).abs(),
            "waypoint {from:?}->{to:?} is not a straight run"
        );
        while from.idx() != to.idx() {
            let (x, y) = t.xy(from.idx());
            let next_idx = t.idx(x + dx, y + dy);
            let next = if to.on_deck() && next_idx == to.idx() {
                Node::deck(next_idx)
            } else if from.on_deck() {
                Node::deck(next_idx)
            } else {
                Node::ground(next_idx)
            };
            assert!(t.can_cross(from, next), "illegal step {from:?}->{next:?}");
            from = next;
        }
    }

    // Follow waypoints from `from` until `to`, asserting each hop is a
    // legal straight run. Returns the hop count.
    fn follow(t: &Terrain, s: &mut Scratch, from: Node, to: Node) -> usize {
        let mut cur = from;
        for hops in 0..512 {
            if cur == to {
                return hops;
            }
            let wp = first_waypoint(t, s, cur, to);
            assert_ne!(wp, NONE, "no path from {cur:?} to {to:?}");
            walk_straight(t, cur, wp);
            cur = wp;
        }
        panic!("no arrival within 512 hops");
    }

    #[test]
    fn routes_cross_the_bridge_not_the_water() {
        let blob = course();
        let t = Terrain::parse(&blob).unwrap();
        let mut s = SCRATCH.lock().unwrap_or_else(|e| e.into_inner());
        // west grass to east grass, near the bridge row: the cheap route
        // boards the deck (swim costs triple)
        let from = Node::ground(t.idx(1, 4));
        let to = Node::ground(t.idx(14, 4));
        let mut hops = Vec::new();
        let mut cur = from;
        while cur != to {
            let wp = first_waypoint(&t, &mut s, cur, to);
            assert_ne!(wp, NONE);
            walk_straight(&t, cur, wp);
            hops.push(wp);
            cur = wp;
            assert!(hops.len() < 64);
        }
        assert!(
            hops.iter().any(|n| n.on_deck()),
            "path should cross the deck: {hops:?}"
        );
    }

    #[test]
    fn swimmers_pass_under_the_bridge() {
        let blob = course();
        let t = Terrain::parse(&blob).unwrap();
        let mut s = SCRATCH.lock().unwrap_or_else(|e| e.into_inner());
        // north river (elev 1) from y=2 to y=6 passes under the deck at y=4
        let from = Node::ground(t.idx(7, 2));
        let to = Node::ground(t.idx(7, 6));
        follow(&t, &mut s, from, to);
    }

    #[test]
    fn waterfall_forces_the_long_way_round() {
        let blob = course();
        let t = Terrain::parse(&blob).unwrap();
        let mut s = SCRATCH.lock().unwrap_or_else(|e| e.into_inner());
        // in-river crossing of the waterfall line must route via a bank
        let from = Node::ground(t.idx(7, 6));
        let to = Node::ground(t.idx(7, 10));
        let mut cur = from;
        let mut left_water = false;
        for _ in 0..64 {
            if cur == to {
                break;
            }
            let wp = first_waypoint(&t, &mut s, cur, to);
            assert_ne!(wp, NONE);
            if t.class(wp.idx()) != mythra_sim_terrain::SWIM {
                left_water = true;
            }
            cur = wp;
        }
        assert_eq!(cur, to);
        assert!(left_water, "must exit the river to pass the waterfall");
    }

    #[test]
    fn unreachable_is_none_and_search_is_repeatable() {
        let mut buf = vec![0u8; HEADER + 64 * BYTES_PER_CELL];
        let mut b = Builder::new(&mut buf, 8, 8);
        for y in 0..8 {
            b.set_ground(4, y, BLOCK, 0); // full wall splits the park
        }
        let blob = b.finish().to_vec();
        let t = Terrain::parse(&blob).unwrap();
        let mut s = SCRATCH.lock().unwrap_or_else(|e| e.into_inner());
        let from = Node::ground(t.idx(1, 1));
        let to = Node::ground(t.idx(6, 6));
        assert_eq!(first_waypoint(&t, &mut s, from, to), NONE);
        // determinism across repeated searches on reused scratch
        let near = Node::ground(t.idx(2, 6));
        let a = first_waypoint(&t, &mut s, from, near);
        for _ in 0..3 {
            assert_eq!(first_waypoint(&t, &mut s, from, near), a);
        }
    }

    #[test]
    fn steering_reaches_waypoints_and_slides_around_barrels() {
        let blob = course();
        let t = Terrain::parse(&blob).unwrap();
        // walk from (0,2) straight through the barrel cell (2,2): the
        // center 2x2 subtiles are blocked, so the dog must slide, and must
        // still arrive.
        let (mut x, mut y) = center(&t, Node::ground(t.idx(0, 2)));
        let mut on_deck = false;
        let wp = Node::ground(t.idx(4, 2));
        let mut arrived = false;
        for _ in 0..400 {
            match step_toward(&t, &mut x, &mut y, &mut on_deck, wp, false) {
                Step::Arrived => {
                    arrived = true;
                    break;
                }
                Step::Moving => {
                    assert!(!t.subtile_blocked(x, y), "steering entered an obstacle");
                }
                Step::Blocked => panic!("wedged against the barrel"),
            }
        }
        assert!(arrived);
    }

    #[test]
    fn steering_respects_cliffs_and_reports_blocked() {
        let blob = course();
        let t = Terrain::parse(&blob).unwrap();
        // aim straight at the plateau wall from below: every crossing into
        // elev 1 is illegal off-stairs, so the dog walks to the wall and
        // then reports Blocked instead of climbing.
        let (mut x, mut y) = center(&t, Node::ground(t.idx(13, 10)));
        let mut on_deck = false;
        let wp = Node::ground(t.idx(13, 13));
        let mut blocked = false;
        for _ in 0..200 {
            match step_toward(&t, &mut x, &mut y, &mut on_deck, wp, false) {
                Step::Blocked => {
                    blocked = true;
                    break;
                }
                Step::Arrived => panic!("climbed a cliff"),
                Step::Moving => {}
            }
        }
        assert!(blocked);
        assert_eq!(y >> 16, 11, "stopped in the last legal row");
    }

    #[test]
    fn steering_boards_the_deck_at_the_waypoint_cell() {
        let blob = course();
        let t = Terrain::parse(&blob).unwrap();
        // stand on the west platform (4,4), waypoint = deck at (5,4)
        let (mut x, mut y) = center(&t, Node::ground(t.idx(4, 4)));
        let mut on_deck = false;
        let wp = Node::deck(t.idx(5, 4));
        for _ in 0..100 {
            if step_toward(&t, &mut x, &mut y, &mut on_deck, wp, false) == Step::Arrived {
                break;
            }
        }
        assert!(
            on_deck,
            "crossing into the waypoint cell adopts its surface"
        );
        assert_eq!((x >> 16, y >> 16), (5, 4));
    }

    #[test]
    fn full_ticks_walk_a_dog_across_the_course() {
        // End to end: waypoint-following + steering, corner by corner,
        // exactly as the park sim drives it — including swim slowdown.
        let blob = course();
        let t = Terrain::parse(&blob).unwrap();
        let mut s = SCRATCH.lock().unwrap_or_else(|e| e.into_inner());
        let target = Node::ground(t.idx(14, 14)); // on the plateau
        let (mut x, mut y) = center(&t, Node::ground(t.idx(0, 0)));
        let mut on_deck = false;
        let mut waypoint = NONE;
        let mut ticks = 0;
        loop {
            ticks += 1;
            assert!(ticks < 3000, "did not arrive");
            let here_idx = t.idx(x >> 16, y >> 16);
            let here = if on_deck {
                Node::deck(here_idx)
            } else {
                Node::ground(here_idx)
            };
            if waypoint == NONE {
                waypoint = first_waypoint(&t, &mut s, here, target);
                assert_ne!(waypoint, NONE);
            }
            match step_toward(&t, &mut x, &mut y, &mut on_deck, waypoint, false) {
                Step::Arrived => {
                    if waypoint == target {
                        break;
                    }
                    waypoint = NONE;
                }
                Step::Moving => {}
                Step::Blocked => waypoint = NONE,
            }
        }
        assert_eq!((x >> 16, y >> 16), (14, 14));
        assert!(!on_deck);
    }
}
