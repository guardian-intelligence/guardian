"""guardian-bench: personal-assistant anticipation benchmark, graded on database state."""

__all__ = ["GuardianBenchTaskset"]


# Lazy so the verifiers plugin loader (which resolves the taskset id
# `guardian_bench` to this package) finds the class, while the hermetic
# stdlib-only Bazel tests can import the package without verifiers installed.
def __getattr__(name):
    if name == "GuardianBenchTaskset":
        from guardian_bench.taskset import GuardianBenchTaskset

        return GuardianBenchTaskset
    raise AttributeError(name)
