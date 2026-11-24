import assert from 'node:assert/strict';
import test from 'node:test';
import {listMultipartParts} from '../src/s3-multipart-parts.mjs';

const part = number => ({number, etag: '"test-' + number + '"', size: 1});
const page = (numbers, truncated = 'false', nextMarker = '') =>
  ({parts: numbers.map(part), truncated, nextMarker});
function reader(...pages) {
  const markers = [];
  return {markers, read: async marker => {
    markers.push(marker);
    assert.ok(pages.length, 'unexpected follow-up ListParts request');
    return pages.shift();
  }};
}
const active = () => true;

test('valid sparse pages advance by the final listed part and retain order', async () => {
  const {read, markers} = reader(page([1, 12], 'true', '12'), page([10000]));
  assert.deepEqual(await listMultipartParts(read, active), [part(1), part(12), part(10000)]);
  assert.deepEqual(markers, [0, 12]);
});

test('a valid empty upload has no parts', async () => {
  const {read, markers} = reader(page([]));
  assert.deepEqual(await listMultipartParts(read, active), []);
  assert.deepEqual(markers, [0]);
});

test('malformed, non-progressing, or mismatched markers stop before a second call', async () => {
  for (const cursor of ['invalid', '', '0', '2.5', '1e2', '10001', '3']) {
    const {read, markers} = reader(page([2], 'true', cursor));
    await assert.rejects(listMultipartParts(read, active), /Invalid part cursor/);
    assert.deepEqual(markers, [0], cursor);
  }
});

test('an empty truncated page or a non-boolean flag is not usable as a part list', async () => {
  for (const input of [page([], 'true', '1'), page([1], 'yes')]) {
    const {read, markers} = reader(input);
    await assert.rejects(listMultipartParts(read, active), /Invalid part/);
    assert.deepEqual(markers, [0]);
  }
});

test('duplicate rows on the later page are rejected rather than returned to a mutation', async () => {
  const {read, markers} = reader(page([1, 2], 'true', '2'), page([2, 3]));
  await assert.rejects(listMultipartParts(read, active), /Invalid part/);
  assert.deepEqual(markers, [0, 2]);
});

test('a stopped view does not fetch another page', async () => {
  const {read, markers} = reader(page([1], 'true', '1'));
  assert.deepEqual(await listMultipartParts(read, () => false), []);
  assert.deepEqual(markers, [0]);
});
