export function median(values) {
  if (values.length === 0) throw new Error("median needs at least one value");
  const sorted = [...values].sort((a, b) => a - b);
  return sorted[Math.floor(sorted.length / 2)];
}

export function metricMedians(runs, metrics) {
  if (runs.length === 0) throw new Error("metricMedians needs at least one run");
  return Object.fromEntries(
    metrics.map((metric) => [metric, median(runs.map((run) => run[metric]))]),
  );
}
