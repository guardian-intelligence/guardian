# Wake Up, Mythra! — Design Pillars

The constitution. Every mechanic, number, and UI string must be defensible
against these. When a feature fights a pillar, the feature loses. Changes to
this file go through PR like everything else.

Status: DRAFT — pillars are author-final prose; rules below each carry a
ratification checkbox until ruled on.

---

## Pillars

### 1. It's About the Dogs

Dogs bring people together, but really it's what dogs represent. They
represent that humanity and nature can coexist — that life can manifest
itself as a being that has no duplicity, just wants to play, and stop and
sniff the flowers, and take a good nap.

This is why we build the game now, as the anxiety around what AI will mean
for the human experience spikes exponentially. AI will never eat your ice
cream for you. Your dog loves you, not your agent.

### 2. An Excuse to Have a Community

The game is really an excuse to have a community — "Pokémon Go" for dog
parks, rewarding long-term loyalty and belonging as a regular member of a
dog park. A counter-force to Bowling Alone.

It should be socially acceptable to go to the dog park "just to get the
bonuses" — as an excuse — and then people happen to meet people who are also
playing. The bonuses should be light. Events inside the game are useful ice
breakers; the focus stays on interpersonal connection.

The job of the game is to turn "I'll go to whatever dog park at whatever
time" into "I will go to this specific dog park at this specific time,
because I know everyone's going to be there." The love of your life might be
showing up every day at 6 and you don't even know it because you keep
showing up at 8.

### 3. Reward Community Pillars, Warmly Embrace Visitors, Provide a Well-Lit Path for Departures

The strongest, most organized and consistent community wins — NOT the ones
who pay the most.

A new dog checking in is unambiguously good for everyone involved. The
regulars' reward is status and being sought after, not power over others.
Visitors and parallel players are whole participants, never second-class:
someone who only ever plants from their couch is still feeding the park.

> **GAP:** the departures clause has no mechanics behind it yet. Moving
> away, drifting off, laying a dog to rest — undesigned as of 2026-08-13.
> It will matter most to someone on the worst day they'll ever have in this
> game. Owns its own design page before launch.

### 4. Never Pay to Win

Premium currency buys entitlements to cosmetics, access to competitions,
and convenience that can be planned around. Nothing else.

Corollary: money can't touch luck — not directly, and not through side
doors like rate-limit bypasses. Every new premium interaction gets audited
against this sentence.

### 5. Plausible Deniability

It should be non-obvious to others that specific players are playing at
specific times, by default. Maddie can log in during a boring meeting, make
some changes, without announcing to her boss — who's also playing during the
meeting — that Maddie logged in. Unless her boss is carefully watching
Maddie's farm and dog inventory: inference should cost attention, and
surveillance should never be free.

This pillar was named late but governed early. It's why remote harvest
exists (a planted seed is a maybe, not a promise), why ripening times are
fuzzy ("~6 PM", never 6:03), why seed detail is gated behind knowing a
dog's name, and why all remote actions publish on the half-hour tick,
mixed together, instead of when they happened.

If you set your activity to Invisible, your management decisions are not
announced — unless a decision affects another player, in which case only
the affected player is notified, on the next tick. The toggle says so
plainly: "Some activity will still notify affected players directly."

---

## Rules

Specific and testable. Each traces to a decision; each needs a ruling.
Mark: **KEEP / REWRITE / STRIKE.**

- [ ] **If you were there, you're in.** Anyone present at an eruption is
      part of it. We never balance the game by excluding someone standing
      at the park.
- [ ] **Your seed is your ticket.** Scheduled a harvest today → you're in
      today's forge, wherever your body is.
- [ ] **Nothing after the pop can join the pop.** Otherwise a forge
      notification becomes a city-wide summons to drive over and tag in.
- [ ] **Nobody's harvest can hurt anyone else's odds.** Checking in at a
      quiet hour never decreases the chances for others; only showing up
      _together_ multiplies them.
- [ ] **Showing up when everyone shows up is the whole game.** Staying
      longer buys nothing. We don't create artificial pressure to stay
      longer than a dog's comfortable — in-and-out in 30 seconds counts.
- [ ] **Real weather only ever protects.** Bad real-world conditions give
      mulligans and reasons to leave, never reasons to stay or come. Heat
      rules fail closed, tuned to dog tolerance, not human comfort.
- [ ] **Helping someone always pays the helper too.** No charity
      mechanics; symbiosis only.
- [ ] **Dogs are forever.** Never deleted, never expire. Packs survive a
      dog that no longer checks in.
- [ ] **The game only announces what the real world already announces.**
      Being at the park is public in reality, so it's public in the game.
      Tapping your phone in a meeting is invisible in reality, so it's
      invisible in the game.
- [ ] **No counterfactual shame.** The game never shows "2 more dogs and
      you'd have made it." Misses are consoled ("you're due"), not
      itemized.

---

## Rejected — kept so we don't re-argue them

- **RSVP / event board.** A pin reading "1 attending" is a public
  rejection artifact. Intent is expressed by planting seeds instead.
- **Hard check-in cutoff before the roll.** Punishes a 6:01 arrival on a
  plan made in good faith. Replaced by overlap membership + sealed seeds.
- **Picking which harvest your presence counts toward.** Fragments the
  crowd — Maddy standing inside an eruption she opted out of. Presence
  counts everywhere it physically occurred.
- **Per-player forge rolls.** Makes a new arrival dilute your share at
  the exact moment of highest emotion. The park rolls; everyone with a
  ticket forges together.
- **Forge eligible every night.** "Every night electric gets old." Three
  tending weeks, one festival week. Scarcity by season reads as ritual;
  scarcity by RNG reads as a slot machine.
- **Referral attribution.** The game never records who recruited whom.
  Word of mouth works because arrival helps everyone, not because the
  recruiter gets paid.
- **Streaks that punish.** A sick dog or a travel week never turns the
  app into a monument to failure.

---

## Deferred

- **Multi-park visitors.** Users who regularly visit different dog parks
  won't be happy; we'll extend the system to support them once we
  understand their needs better. (Verbatim tradeoff from day one —
  revisit after launch parks are healthy.)

---

_Vocabulary lives in the glossary (Trainer, Dog, Pack, Dog Park, Check In,
Mythra, Mythraforge, Rosieforge, the Pile, tending weeks, festival week).
Numbers live in the parameter sheet, not in prose. System mechanics each
get a one-pager that cites the pillar it serves._
