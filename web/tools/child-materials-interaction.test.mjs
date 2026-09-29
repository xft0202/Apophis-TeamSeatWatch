import { test } from 'node:test';
import assert from 'node:assert/strict';
import { exportRange, togglePage } from '../src/rebuild/owner/childSelection.ts';

test('page selection keeps earlier pages without silently selecting filtered results', () => {
  const first = togglePage(new Set(), ['a', 'b']);
  const second = togglePage(first, ['c', 'd']);
  assert.deepEqual(exportRange('selected', second, '@example.com', 400), {
    scope: 'selected', accountIds: ['a', 'b', 'c', 'd'], search: '', count: 4,
  });
  assert.deepEqual([...togglePage(second, ['c', 'd'])].sort(), ['a', 'b']);
  assert.deepEqual(exportRange('filtered', second, '@example.com', 400), {
    scope: 'filtered', accountIds: [], search: '@example.com', count: 400,
  });
});
