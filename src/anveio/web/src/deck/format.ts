// Compact slide units: negatives in parentheses.

const sig = (n: number) => String(Number(n.toPrecision(3)));

export const money = (value: number): string => {
  const v = Math.abs(value);
  let text: string;
  if (v >= 999_500_000_000) text = `$${sig(v / 1_000_000_000_000)}T`;
  else if (v >= 999_500_000) text = `$${sig(v / 1_000_000_000)}B`;
  else if (v >= 999_500) text = `$${sig(v / 1_000_000)}M`;
  else if (v >= 1_000) text = `$${Math.round(v / 1_000)}k`;
  else text = `$${Math.round(v)}`;
  return value < -0.5 ? `(${text})` : text;
};
