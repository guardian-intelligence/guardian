"""SQLite life-database: build from a scenario seed, apply agent SQL, snapshot for diffing."""

import re
import sqlite3
from datetime import datetime
from pathlib import Path

USER_TABLES = (
    "places",
    "person",
    "contacts",
    "events",
    "items",
    "reminders",
    "event_prep",
    "assistant_questions",
)
WORLD_TABLES = ("forecast", "venue_hours", "transit")

SCHEMA_PATH = Path(__file__).parent / "schema.sql"
_SQL_FENCE = re.compile(r"```sql\s*\n(.*?)```", re.DOTALL | re.IGNORECASE)


def build_db(seed: dict) -> sqlite3.Connection:
    conn = sqlite3.connect(":memory:")
    conn.executescript(SCHEMA_PATH.read_text())
    for table in USER_TABLES + WORLD_TABLES:
        for row in seed.get(table, []):
            cols = ", ".join(row)
            marks = ", ".join("?" for _ in row)
            conn.execute(
                f"INSERT INTO {table} ({cols}) VALUES ({marks})",
                list(row.values()),
            )
    conn.commit()
    return conn


def snapshot(conn: sqlite3.Connection) -> dict[str, list[tuple]]:
    return {
        table: sorted(conn.execute(f"SELECT * FROM {table}").fetchall())
        for table in USER_TABLES + WORLD_TABLES
    }


def extract_sql(reply: str) -> str | None:
    """Take the last ```sql fence in the reply; models often think aloud before it."""
    blocks = _SQL_FENCE.findall(reply or "")
    return blocks[-1].strip() if blocks else None


def apply_sql(conn: sqlite3.Connection, sql: str) -> str | None:
    try:
        conn.executescript(sql)
        conn.commit()
        return None
    except sqlite3.Error as exc:
        conn.rollback()
        return str(exc)


def parse_ts(value: str) -> datetime:
    return datetime.fromisoformat(value)


def dump_table(conn: sqlite3.Connection, table: str) -> str:
    cols = [c[1] for c in conn.execute(f"PRAGMA table_info({table})")]
    rows = conn.execute(f"SELECT * FROM {table}").fetchall()
    lines = [f"### {table} ({', '.join(cols)})"]
    lines += ["  " + " | ".join("NULL" if v is None else str(v) for v in row) for row in rows]
    if not rows:
        lines.append("  (empty)")
    return "\n".join(lines)


def render_state(conn: sqlite3.Connection) -> str:
    return "\n\n".join(dump_table(conn, t) for t in USER_TABLES + WORLD_TABLES)
