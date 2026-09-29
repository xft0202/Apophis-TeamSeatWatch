import { test } from 'node:test';
import assert from 'node:assert/strict';
import { togglePage } from '../src/rebuild/owner/childSelection.ts';
import { frozenStandbyPreview, standbySelectionRequest } from '../src/rebuild/owner/standbySelection.ts';

test('cross-page checkboxes and explicit all-current-filter do not conflate ranges', () => {
  const selected = togglePage(togglePage(new Set(), ['first', 'second']), ['third']);
  assert.deepEqual(standbySelectionRequest('selected', selected, '@other.test', 'batch'), {
    scope: 'selected', accountIds: ['first', 'second', 'third'],
  });
  assert.deepEqual(standbySelectionRequest('filtered', selected, '@other.test', 'batch'), {
    scope: 'filtered', search: '@other.test',
  });
  assert.deepEqual(standbySelectionRequest('batch', selected, '@other.test', 'batch'), {
    scope: 'batch', batchId: 'batch',
  });
  assert.deepEqual([...togglePage(selected, ['third'])].sort(), ['first', 'second']);
  assert.throws(() => standbySelectionRequest('batch', selected, '', undefined), /batch id required/);
});

test('mocked delayed preview cannot restore a confirmation after filter changes', async () => {
  let finish;
  let generation = 1;
  let calls = 0;
  const result = frozenStandbyPreview(() => { calls++; return new Promise((resolve) => { finish = resolve; }); }, generation, () => generation);
  generation++;
  finish({ count: 3, members: ['old-filter'] });
  assert.equal(await result, null);
  assert.equal(calls, 1);
  assert.deepEqual(await frozenStandbyPreview(async () => ({ count: 1, members: ['current-filter'] }), generation, () => generation), { count: 1, members: ['current-filter'] });
});
