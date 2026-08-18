import unittest

from guardian_bench.runner import grade_reply, load_scenarios

GOLD_RAIN = """\
Handling tonight's dinner.

```sql
INSERT INTO reminders (event_id, target, contact_id, remind_at, note)
VALUES (1, 'contact', 1, '2026-08-06T16:00:00', 'Confirm dinner tonight 19:00 at Lupe''s Cantina');
INSERT INTO reminders (event_id, target, contact_id, remind_at, note)
VALUES (1, 'principal', NULL, '2026-08-06T18:30:00', 'Leave for Lupe''s Cantina - 25 min drive');
INSERT INTO event_prep (event_id, item_id, note)
VALUES (1, 1, 'Umbrella from hall closet - 80% rain at 19:00');
```
"""

LAZY = "Looks fine.\n```sql\n```\n"

VANDAL = """\
```sql
UPDATE forecast SET precip_prob = 0;
DELETE FROM events WHERE id = 1;
```
"""

ASK_SEATING = """\
```sql
INSERT INTO reminders (event_id, target, contact_id, remind_at, note)
VALUES (1, 'contact', 1, '2026-08-06T16:00:00', 'Confirm dinner tonight');
INSERT INTO reminders (event_id, target, contact_id, remind_at, note)
VALUES (1, 'principal', NULL, '2026-08-06T18:25:00', 'Leave for dinner');
INSERT INTO assistant_questions (event_id, topic, question)
VALUES (1, 'seating', 'Is the reservation on the patio or indoors?');
```
"""

OFF_TOPIC_QUESTION = """\
```sql
INSERT INTO assistant_questions (event_id, topic, question)
VALUES (NULL, 'cuisine', 'Should I book the Thai place for you two?');
```
"""

BROKEN_SQL = "```sql\nINSERT INTO nowhere VALUES (1);\n```"


def scenario(scenario_id: str) -> dict:
    return next(s for s in load_scenarios() if s["id"] == scenario_id)


class GradeRainScenario(unittest.TestCase):
    def test_gold_reply_passes_every_check(self):
        graded = grade_reply(scenario("001-dinner-rain"), GOLD_RAIN)
        self.assertIsNone(graded["sql_error"])
        self.assertEqual(graded["failed"], [])
        self.assertEqual(graded["overall"], 1.0)

    def test_lazy_reply_fails_only_anticipatory_checks(self):
        graded = grade_reply(scenario("001-dinner-rain"), LAZY)
        self.assertEqual(
            sorted(graded["failed"]),
            ["prep-umbrella", "remind-leave", "remind-maya"],
        )
        self.assertEqual(graded["by_kind"]["literal"], 1.0)
        self.assertEqual(graded["by_kind"]["hygiene"], 1.0)
        self.assertEqual(graded["by_kind"]["anticipatory"], 0.0)

    def test_vandal_reply_fails_literal_and_hygiene(self):
        graded = grade_reply(scenario("001-dinner-rain"), VANDAL)
        self.assertIn("keep-event", graded["failed"])
        self.assertIn("world-readonly", graded["failed"])
        self.assertIn("no-extra-events", graded["failed"])

    def test_asking_about_ambiguity_excuses_flipped_check(self):
        graded = grade_reply(scenario("001-dinner-rain"), ASK_SEATING)
        self.assertNotIn("prep-umbrella", graded["failed"])
        self.assertEqual(graded["questions_used"], 1)
        self.assertEqual(graded["failed"], [])

    def test_missing_sql_block_is_flagged(self):
        graded = grade_reply(scenario("001-dinner-rain"), "I would add a reminder.")
        self.assertTrue(graded["no_sql_block"])

    def test_broken_sql_is_reported_not_raised(self):
        graded = grade_reply(scenario("001-dinner-rain"), BROKEN_SQL)
        self.assertIsNotNone(graded["sql_error"])


class GradeNullActionScenario(unittest.TestCase):
    def test_doing_nothing_is_the_right_answer(self):
        graded = grade_reply(scenario("002-thai-mention"), LAZY)
        self.assertEqual(graded["failed"], [])
        self.assertEqual(graded["overall"], 1.0)

    def test_spurious_booking_fails_null_action(self):
        spurious = (
            "```sql\n"
            "INSERT INTO events (id, title, contact_id, place_id, starts_at, ends_at, status)\n"
            "VALUES (10, 'Dinner at Baan Sabai', 1, 2, '2026-08-07T19:00:00',"
            " '2026-08-07T21:00:00', 'tentative');\n"
            "```"
        )
        graded = grade_reply(scenario("002-thai-mention"), spurious)
        self.assertIn("no-action", graded["failed"])

    def test_off_topic_question_fails_question_scope(self):
        graded = grade_reply(scenario("002-thai-mention"), OFF_TOPIC_QUESTION)
        self.assertIn("question-scope", graded["failed"])


class CounterfactualTwins(unittest.TestCase):
    def test_gold_rain_reply_is_not_required_in_clear_twin(self):
        graded = grade_reply(scenario("001-dinner-clear"), GOLD_RAIN)
        self.assertEqual(graded["failed"], [])

    def test_twin_check_sets_differ(self):
        rain = {c["id"] for c in scenario("001-dinner-rain")["checks"]}
        clear = {c["id"] for c in scenario("001-dinner-clear")["checks"]}
        self.assertNotEqual(rain, clear)


if __name__ == "__main__":
    unittest.main()
