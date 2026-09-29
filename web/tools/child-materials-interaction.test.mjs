import { test } from 'node:test';
import assert from 'node:assert/strict';
import { exportRange, togglePage } from '../src/rebuild/owner/childSelection.ts';
import { visiblePersonalStatus } from '../src/rebuild/owner/personalSessionStatus.ts';

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

test('a saved Personal session stops displaying ready when its expiry passes', () => {
  const expiresAt = '2026-09-29T12:00:00Z';
  const expiry = Date.parse(expiresAt);
  assert.equal(visiblePersonalStatus('ready', expiresAt, expiry - 1), 'ready');
  assert.equal(visiblePersonalStatus('ready', expiresAt, expiry), 'session_expired');
  assert.equal(visiblePersonalStatus('ready', expiresAt, expiry + 1), 'session_expired');
  assert.equal(visiblePersonalStatus('ready', 'not-a-date', expiry - 1), 'session_expired');
  assert.equal(visiblePersonalStatus('credential_invalid', expiresAt, expiry - 1), 'credential_invalid');
});
