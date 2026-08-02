"""verifiers v1 taskset: one task per scenario, graded by predicates over the state diff."""

import verifiers.v1 as vf

from guardian_bench.runner import (
    SYSTEM_PROMPT,
    grade_reply,
    load_scenarios,
    task_prompt,
)


class GuardianBenchData(vf.TaskData):
    scenario: dict


class GuardianBenchTask(vf.Task[GuardianBenchData]):
    @vf.reward
    async def predicates(self, trace: vf.Trace) -> float:
        graded = grade_reply(self.data.scenario, trace.last_reply or "")
        if graded["no_sql_block"] or graded["sql_error"]:
            return 0.0
        return graded["overall"]


class GuardianBenchTaskset(vf.Taskset[GuardianBenchTask, vf.TasksetConfig]):
    def load(self) -> list[GuardianBenchTask]:
        return [
            GuardianBenchTask(
                GuardianBenchData(
                    idx=idx,
                    name=scenario["id"],
                    prompt=task_prompt(scenario),
                    system_prompt=SYSTEM_PROMPT,
                    scenario=scenario,
                ),
                self.config.task,
            )
            for idx, scenario in enumerate(load_scenarios())
        ]


__all__ = ["GuardianBenchTaskset"]
