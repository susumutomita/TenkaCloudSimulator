// Presentation only: canonical statistic names remain native API/data keys.
const japaneseStatistics = Object.freeze({
  Average: '平均',
  Sum: '合計',
  Minimum: '最小',
  Maximum: '最大',
  SampleCount: 'サンプル数',
});

export function metricStatisticLabel(value, locale) {
  return locale === 'ja' && Object.hasOwn(japaneseStatistics, value)
    ? `${japaneseStatistics[value]} (${value})`
    : value;
}
