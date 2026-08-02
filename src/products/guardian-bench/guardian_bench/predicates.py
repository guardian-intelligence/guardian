"""Predicate engine: necessary-condition checks over a before/after database pair.

Checks grade outcomes in the final state, never the mechanism that produced
them. A check flipped by an authored ambiguity is excused when the agent asked
about that ambiguity's topic instead of guessing (single-turn ask-vs-act).
"""

import sqlite3
from dataclasses import dataclass
from datetime import timedelta

from guardian_bench.world import USER_TABLES, WORLD_TABLES, parse_ts, snapshot

KINDS = ("literal", "anticipatory", "hygiene", "question")


@dataclass
class CheckResult:
    check_id: str
    kind: str
    passed: bool
    asked: bool
    detail: str


def _rows(conn: sqlite3.Connection, table: str, where: dict) -> list[tuple]:
    clause = " AND ".join(f"{col} = ?" for col in where)
    query = f"SELECT * FROM {table}" + (f" WHERE {clause}" if where else "")
    return conn.execute(query, list(where.values())).fetchall()


def _event(conn: sqlite3.Connection, event_id: int) -> dict | None:
    row = conn.execute(
        "SELECT id, title, contact_id, place_id, starts_at, ends_at, status"
        " FROM events WHERE id = ?",
        (event_id,),
    ).fetchone()
    if row is None:
        return None
    keys = ("id", "title", "contact_id", "place_id", "starts_at", "ends_at", "status")
    return dict(zip(keys, row))


def _check_row_unchanged(before, after, params, scenario):
    old = _rows(before, params["table"], params.get("where", {}))
    new = _rows(after, params["table"], params.get("where", {}))
    return old == new, f"{params['table']} rows changed" if old != new else "unchanged"


def _check_reminder_exists(before, after, params, scenario):
    event = _event(after, params["event_id"])
    if event is None:
        return False, f"event {params['event_id']} missing"
    start = parse_ts(event["starts_at"])
    lead = timedelta(minutes=params.get("min_lead_minutes", 0))
    now = parse_ts(scenario["meta"]["now"])
    where = {"event_id": params["event_id"], "target": params["target"]}
    if params.get("contact_id") is not None:
        where["contact_id"] = params["contact_id"]
    for row in _rows(after, "reminders", where):
        remind_at = parse_ts(row[4])
        if now <= remind_at <= start - lead:
            return True, f"reminder at {row[4]}"
    return False, "no qualifying reminder"


def _check_leave_by_reminder(before, after, params, scenario):
    event = _event(after, params["event_id"])
    if event is None:
        return False, f"event {params['event_id']} missing"
    person = after.execute("SELECT home_place_id FROM person LIMIT 1").fetchone()
    minutes = after.execute(
        "SELECT MIN(minutes) FROM transit WHERE from_place_id = ? AND to_place_id = ?",
        (person[0], event["place_id"]),
    ).fetchone()[0]
    if minutes is None:
        return False, "no transit row for this trip"
    leave_by = parse_ts(event["starts_at"]) - timedelta(minutes=minutes)
    for row in _rows(after, "reminders", {"event_id": params["event_id"], "target": "principal"}):
        if parse_ts(row[4]) <= leave_by:
            return True, f"leave-by reminder at {row[4]} (needed {leave_by.isoformat()})"
    return False, f"no principal reminder at or before {leave_by.isoformat()}"


def _check_prep_exists(before, after, params, scenario):
    name = params["item_name"].lower()
    rows = after.execute(
        "SELECT ep.id, i.name, ep.note FROM event_prep ep"
        " LEFT JOIN items i ON i.id = ep.item_id WHERE ep.event_id = ?",
        (params["event_id"],),
    ).fetchall()
    for _, item_name, note in rows:
        if name in (item_name or "").lower() or name in (note or "").lower():
            return True, f"prep found: {item_name or note}"
    return False, f"no prep mentioning '{params['item_name']}'"


def _check_world_readonly(before, after, params, scenario):
    changed = [
        t
        for t in WORLD_TABLES
        if snapshot(before)[t] != snapshot(after)[t]
    ]
    return not changed, f"world tables written: {changed}" if changed else "world untouched"


def _check_no_writes(before, after, params, scenario):
    changed = [
        t for t in params["tables"] if snapshot(before)[t] != snapshot(after)[t]
    ]
    return not changed, f"unwanted writes: {changed}" if changed else "no writes"


def _check_no_extra_events(before, after, params, scenario):
    old = {r[0] for r in before.execute("SELECT id FROM events")}
    new = {r[0] for r in after.execute("SELECT id FROM events")}
    return old == new, f"event ids changed: {sorted(new ^ old)}" if old != new else "event set stable"


def _check_question_scope(before, after, params, scenario):
    valid = {a["topic"] for a in scenario.get("ambiguities", [])}
    off_topic = [
        row[2]
        for row in after.execute("SELECT * FROM assistant_questions")
        if row[2] not in valid
    ]
    return not off_topic, f"questions outside authored ambiguities: {off_topic}" if off_topic else "questions in scope"


_CHECKS = {
    "row_unchanged": ("literal", _check_row_unchanged),
    "reminder_exists": ("anticipatory", _check_reminder_exists),
    "leave_by_reminder": ("anticipatory", _check_leave_by_reminder),
    "prep_exists": ("anticipatory", _check_prep_exists),
    "world_readonly": ("hygiene", _check_world_readonly),
    "no_writes": ("hygiene", _check_no_writes),
    "no_extra_events": ("hygiene", _check_no_extra_events),
    "question_scope": ("question", _check_question_scope),
}

KNOWN_CHECK_TYPES = frozenset(_CHECKS)


def asked_topics(after: sqlite3.Connection) -> set[str]:
    return {row[2] for row in after.execute("SELECT * FROM assistant_questions")}


def run_checks(
    before: sqlite3.Connection, after: sqlite3.Connection, scenario: dict
) -> list[CheckResult]:
    topics = asked_topics(after)
    excused = {
        check_id
        for amb in scenario.get("ambiguities", [])
        if amb["topic"] in topics
        for check_id in amb["flips"]
    }
    results = []
    for check in scenario["checks"]:
        kind, fn = _CHECKS[check["type"]]
        passed, detail = fn(before, after, check, scenario)
        if not passed and check["id"] in excused:
            results.append(CheckResult(check["id"], kind, True, True, "asked instead of acting"))
        else:
            results.append(CheckResult(check["id"], kind, passed, False, detail))
    return results


def score(results: list[CheckResult]) -> dict:
    by_kind = {}
    for kind in KINDS:
        of_kind = [r for r in results if r.kind == kind]
        if of_kind:
            by_kind[kind] = sum(r.passed for r in of_kind) / len(of_kind)
    overall = sum(r.passed for r in results) / len(results) if results else 0.0
    return {
        "overall": overall,
        "by_kind": by_kind,
        "questions_used": sum(r.asked for r in results),
        "failed": [r.check_id for r in results if not r.passed],
    }
