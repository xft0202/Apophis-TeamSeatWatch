import { test } from 'node:test';
import assert from 'node:assert/strict';
import { personalProbeScope } from '../src/owner/personalProbeScope.ts';
import { PersonalPreviewGate } from '../src/owner/personalPreviewGate.ts';

test('personal probe selected IDs take precedence across pages and exclude the search filter', () => {
  const selected = new Set(['page-2', 'page-1']);
  assert.deepEqual(personalProbeScope(selected, 'ignored-filter'), { targetAccountIds: ['page-1', 'page-2'] });
  assert.deepEqual(personalProbeScope(new Set(), 'all-matches'), { search: 'all-matches' });
});

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}

test('Personal preview ignores out-of-order and scope-changed responses', async () => {
  const gate = new PersonalPreviewGate();
  const received = [];
  const first = deferred();
  const second = deferred();
  const firstResult = gate.load('filtered:old', () => first.promise, (value) => received.push(value), () => received.push('older error'), () => {});
  const secondResult = gate.load('filtered:old', () => second.promise, (value) => received.push(value), () => received.push('newer error'), () => {});
  second.resolve('newer preview');
  await secondResult;
  first.resolve('late older preview');
  await firstResult;
  assert.deepEqual(received, ['newer preview']);

  const third = deferred();
  const thirdResult = gate.load('filtered:old', () => third.promise, (value) => received.push(value), () => received.push('stale error'), () => {});
  gate.updateScope('selected:account-on-another-page');
  third.resolve('late previous scope');
  await thirdResult;
  assert.deepEqual(received, ['newer preview']);

  const fourth = deferred();
  const fourthResult = gate.load('filtered:new', () => fourth.promise, (value) => received.push(value), () => received.push('outdated failure'), () => {});
  gate.updateScope('selected:new-account');
  fourth.reject(new Error('late network error'));
  await fourthResult;
  assert.deepEqual(received, ['newer preview']);
});
