import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import {servicesForProvider,serviceAvailable} from '../../tools/console/src/console-navigation.mjs';
const source=readFileSync(new URL('../../tools/console/src/console-home.jsx',import.meta.url),'utf8');
const match=source.match(/export const SERVICE_PRESENTATION = Object.freeze\((\{[\s\S]*?\n\})\);/);
assert.ok(match,'Actual presentation registry must be available');
const presentation=vm.runInNewContext('('+match[1]+')');
test('WAF is discoverable in Security and search without widening scoped sessions',()=>{
 const capabilities={services:{wafv2:{scope:'REGIONAL'},ssm:{}}};
 const services=servicesForProvider('aws',capabilities);
 assert.deepEqual(services.filter(item=>presentation[item.id]?.category==='security').map(item=>item.id),['wafv2']);
 assert.equal(presentation.wafv2.color,'red');
 for(const query of ['WAF','ウェブ'])assert.ok(servicesForProvider('aws',capabilities,query).some(item=>item.id==='wafv2'));
 const scoped={...capabilities,consoleScope:{region:'us-east-1',parameterName:'/team/value'}};
 assert.equal(serviceAvailable('wafv2',scoped),false);
 assert.deepEqual(servicesForProvider('aws',scoped).map(item=>item.id),['ssm']);
});
