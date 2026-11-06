import test from 'node:test';
import assert from 'node:assert/strict';
import {metricStatisticLabel} from '../../tools/console/src/metrics-presentation.mjs';

const statistics = Object.freeze(['Average','Sum','Minimum','Maximum','SampleCount']);
const japanese = ['平均 (Average)','合計 (Sum)','最小 (Minimum)','最大 (Maximum)','サンプル数 (SampleCount)'];

test('all five native statistic keys retain their exact English label and expose Japanese labels', () => {
  assert.deepEqual(statistics.map(s=>metricStatisticLabel(s,'en')), statistics);
  assert.deepEqual(statistics.map(s=>metricStatisticLabel(s,'ja')), japanese);
});

test('locale changes presentation without rewriting selected values or native datapoint lookups', () => {
  const selected = Object.freeze({value:'Average',label:'Average'});
  const datapoint = Object.freeze({Average:2,Sum:6,Minimum:1,Maximum:3,SampleCount:3});
  const before = JSON.stringify({selected,datapoint,statistics});
  for (const locale of ['en','ja','en']) {
    const options = statistics.map(value=>({value,label:metricStatisticLabel(value,locale)}));
    assert.deepEqual(options.map(o=>o.value), statistics);
    assert.equal(options.find(o=>o.value===selected.value).label,locale==='ja'?'平均 (Average)':'Average');
    assert.equal(datapoint[selected.value],2);
    assert.equal(JSON.stringify({selected,datapoint,statistics}),before);
  }
});

test('unrecognized locale and user/native identifiers are never invented translation keys', () => {
  for (const locale of [undefined,'fr','JA','ja'])
    for (const value of ['custom.Metric','Count','Average/Second','average','constructor','__proto__',''])
      assert.equal(metricStatisticLabel(value,locale),value);
  assert.equal(metricStatisticLabel('Average','fr'),'Average');
});
