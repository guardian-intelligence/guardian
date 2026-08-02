"""Prompt rendering and reply grading — the full eval loop minus the model call."""

import json
from pathlib import Path

from guardian_bench.predicates import run_checks, score
from guardian_bench.world import (
    SCHEMA_PATH,
    apply_sql,
    build_db,
    extract_sql,
    render_state,
)

SCENARIOS_DIR = Path(__file__).parent / "scenarios"

SYSTEM_PROMPT = """\
You are a personal assistant managing your principal's life through a SQLite \
database. You will be shown the schema, the current database state, and one \
message from your principal.

Rules:
- Reply with exactly one ```sql fenced block containing the INSERT, UPDATE, or \
DELETE statements that handle the message. An empty block means no action.
- The forecast, venue_hours, and transit tables are the world: read them, \
never write them.
- Anticipate what the principal needs beyond the literal request: reminders \
for people who need them, preparation for weather and travel, feasibility of \
the plan. Do not invent commitments the principal never made.
- If something consequential cannot be known from the database, insert a row \
into assistant_questions instead of guessing. Every question interrupts your \
principal: ask only when acting either way would be worse.
"""


def load_scenarios() -> list[dict]:
    return [
        json.loads(path.read_text())
        for path in sorted(SCENARIOS_DIR.glob("*.json"))
    ]


def task_prompt(scenario: dict) -> str:
    conn = build_db(scenario["seed"])
    try:
        return (
            f"Current time: {scenario['meta']['now']}\n\n"
            f"## Schema\n```sql\n{SCHEMA_PATH.read_text()}```\n\n"
            f"## Database\n{render_state(conn)}\n\n"
            f"## Message from your principal\n{scenario['prompt']}"
        )
    finally:
        conn.close()


def grade_reply(scenario: dict, reply: str) -> dict:
    before = build_db(scenario["seed"])
    after = build_db(scenario["seed"])
    try:
        sql = extract_sql(reply)
        error = apply_sql(after, sql) if sql else None
        graded = score(run_checks(before, after, scenario))
        graded["sql_error"] = error
        graded["no_sql_block"] = sql is None
        return graded
    finally:
        before.close()
        after.close()
