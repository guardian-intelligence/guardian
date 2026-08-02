# guardian-bench

A personal-assistant anticipation benchmark. One task is one message from a
principal to their assistant; the assistant's answer is a set of writes to a
SQLite database that models the principal's life. Grading compares the final
database state against necessary-condition predicates — never against a single
gold answer, and never against the mechanism (tool calls, reasoning style) that
produced the state.

## What it measures

A scheduling request is easy to comply with and hard to *serve*. Given "make
sure tonight's dinner goes smoothly," a compliant assistant does nothing wrong;
a good assistant checks the evening forecast, stages the umbrella, reminds the
friend who always forgets, and works backwards from the drive time — without
being asked, and without interrogating the principal about things the database
already answers.

Each scenario grades four separable behaviors:

- **literal** — the stated request is honored and existing commitments survive.
- **anticipatory** — needs implied by world state (weather, travel time,
  counterpart reliability) are handled unprompted. This is the headline axis.
- **hygiene** — the world is read-only, and no commitments are invented. A
  conversational mention is not an instruction (null-action scenarios).
- **question** — when a consequential fact is genuinely unknowable from the
  database, asking beats guessing; asking about anything else is a wasted
  interruption. Authored ambiguities define which is which, and a question on
  an ambiguity's topic excuses the checks that ambiguity flips.

Scenarios ship in counterfactual pairs (`twin`): the same prompt over flipped
world state must demand different behavior, so an assistant that pattern-matches
the prompt without conditioning on the world cannot pass both.

## Layout

- `guardian_bench/schema.sql` — the life-database: user tables the assistant
  may write, world tables (`forecast`, `venue_hours`, `transit`) it may only read.
- `guardian_bench/scenarios/*.json` — seed state, principal prompt, authored
  ambiguities, and the predicate list per scenario.
- `guardian_bench/predicates.py` — the check engine over before/after snapshots.
- `guardian_bench/runner.py` — prompt rendering and reply grading (stdlib only).
- `guardian_bench/taskset.py` — verifiers v1 `Taskset` adapter.

## Running

Grading logic is tested hermetically via Bazel:

```sh
bazelisk test //src/products/guardian-bench/...
```

Model evaluation uses [verifiers](https://github.com/PrimeIntellect-ai/verifiers)
with the repo-pinned `uv` (`aspect tools install`):

```sh
cd src/products/guardian-bench
uv sync
```

`guardian_bench.taskset.GuardianBenchTaskset` is the verifiers v1 entrypoint;
eval configs for specific models land alongside the first scored runs.

Scenario authoring rules, in brief: every check must be computable from the
database pair alone; every ambiguity must flip at least one check; every
scenario with weather- or world-conditioned checks needs a twin; timing
constraints are expressed as information availability in the world tables, not
as required tool-call sequences.
