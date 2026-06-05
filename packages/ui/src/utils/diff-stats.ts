export function formatDiffStat(value: number): string {
  const abs = Math.abs(value);
  if (abs < 1000) return String(value);

  const divisor = abs >= 999_500 ? 1_000_000 : 1000;
  const suffix = divisor === 1_000_000 ? "M" : "k";
  const scaled = value / divisor;
  const integerDigits = Math.max(1, Math.floor(Math.abs(scaled)).toString().length);
  const fractionDigits = Math.max(0, 3 - integerDigits);
  const compact = Number(scaled.toFixed(fractionDigits)).toString();

  return `${compact}${suffix}`;
}
