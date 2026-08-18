import unittest

from guardian_bench.predicates import KNOWN_CHECK_TYPES
from guardian_bench.runner import load_scenarios, task_prompt
from guardian_bench.world import build_db


class ScenarioStructure(unittest.TestCase):
    def setUp(self):
        self.scenarios = load_scenarios()
        self.by_id = {s["id"]: s for s in self.scenarios}

    def test_scenarios_exist_with_unique_ids(self):
        self.assertGreaterEqual(len(self.scenarios), 3)
        self.assertEqual(len(self.by_id), len(self.scenarios))

    def test_every_check_type_is_known_and_ids_unique(self):
        for s in self.scenarios:
            ids = [c["id"] for c in s["checks"]]
            self.assertEqual(len(ids), len(set(ids)), s["id"])
            for check in s["checks"]:
                self.assertIn(check["type"], KNOWN_CHECK_TYPES, s["id"])

    def test_ambiguity_flips_reference_real_checks(self):
        for s in self.scenarios:
            check_ids = {c["id"] for c in s["checks"]}
            for amb in s["ambiguities"]:
                self.assertTrue(set(amb["flips"]) <= check_ids, s["id"])

    def test_twins_are_symmetric_and_divergent(self):
        for s in self.scenarios:
            if s["twin"] is None:
                continue
            twin = self.by_id[s["twin"]]
            self.assertEqual(twin["twin"], s["id"])
            self.assertNotEqual(
                {c["id"] for c in s["checks"]},
                {c["id"] for c in twin["checks"]},
                f"twins {s['id']}/{twin['id']} must demand different behavior",
            )

    def test_seeds_build_with_foreign_keys_intact(self):
        for s in self.scenarios:
            conn = build_db(s["seed"])
            violations = conn.execute("PRAGMA foreign_key_check").fetchall()
            conn.close()
            self.assertEqual(violations, [], s["id"])

    def test_prompts_render(self):
        for s in self.scenarios:
            rendered = task_prompt(s)
            self.assertIn(s["prompt"], rendered)
            self.assertIn("## Database", rendered)


if __name__ == "__main__":
    unittest.main()
