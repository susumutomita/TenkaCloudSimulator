import test from 'node:test';
import assert from 'node:assert/strict';
import {SERVICES, serviceAvailable, servicesForProvider, serviceFromHash, hashForService, providerForService} from '../../tools/console/src/console-navigation.mjs';

const contract = () => ({available:true, console:true, apiVersion:'2010-08-01', protocol:'Query', serviceSelector:'monitoring', operations:['PutMetricData','ListMetrics','GetMetricStatistics']});
const capabilities = metric => ({services:{cloudwatch:metric}, cloudStorage:{gcp:{project:'dummy-project'}, azure:{account:'dummyaccount'}}});

test('Metrics discovery requires its exact bounded native console contract', () => {
  const enabled = capabilities(contract());
  const before = JSON.stringify(enabled);
  assert.equal(serviceAvailable('cloudwatch-metrics', enabled), true);
  assert.equal(JSON.stringify(enabled), before);
  assert.equal(serviceAvailable('cloudwatch-metrics', capabilities({...contract(), operations:[...contract().operations].reverse()})), true);
  for (const missing of [undefined, null, {}, {available:true}, {...contract(), console:false}])
    assert.equal(serviceAvailable('cloudwatch-metrics', capabilities(missing)), false);
  for (const [field, wrong] of [['available',1],['console','true'],['apiVersion','2010-05-08'],['protocol','JSON'],['serviceSelector','cloudwatch']]) {
    assert.equal(serviceAvailable('cloudwatch-metrics', capabilities({...contract(), [field]:wrong})), false);
    const absent = contract(); delete absent[field];
    assert.equal(serviceAvailable('cloudwatch-metrics', capabilities(absent)), false);
  }
  for (const operations of [undefined, 'ListMetrics', [], ['ListMetrics'], ['PutMetricData','ListMetrics','ListMetrics'], [...contract().operations,'GetMetricData'], ['PutMetricData','ListMetrics','GetMetricData']])
    assert.equal(serviceAvailable('cloudwatch-metrics', capabilities({...contract(), operations})), false);
});

test('Metrics navigation remains AWS-scoped, searchable and unavailable without its contract', () => {
  const enabled = capabilities(contract());
  assert.equal(providerForService('cloudwatch-metrics'),'aws');
  assert.equal(hashForService('cloudwatch-metrics'),'/cloudwatch-metrics');
  assert.equal(serviceFromHash('#/cloudwatch-metrics'),'cloudwatch-metrics');
  assert.equal(serviceFromHash('#/cloudwatch-metrics-fake'),null);
  for (const query of ['CloudWatch Metrics','メトリクス','namespace','統計'])
    assert.deepEqual(servicesForProvider('aws',enabled,query).map(s=>s.id),['cloudwatch-metrics']);
  for (const provider of ['gcp','azure','sakura'])
    assert.deepEqual(servicesForProvider(provider,enabled,'メトリクス'),[]);
  assert.deepEqual(servicesForProvider('aws',capabilities(undefined),'メトリクス'),[]);
  assert.equal(SERVICES.filter(s=>s.id==='cloudwatch-metrics').length,1);
  assert.equal(SERVICES.some(s=>s.id==='gcp-firestore'),false);
});
