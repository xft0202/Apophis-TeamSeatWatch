import test from 'node:test';
import assert from 'node:assert/strict';
import { canShowWorkspaceFacts } from '../src/rebuild/owner/workspaceVerification.ts';

const fact = {
  status: 'verified', permission: 'manage', accessStatus: 'readable',
  completeness: 'complete', expiresAt: '2026-01-08T00:00:00Z',
};

test('verified facts need current, complete management evidence', () => {
  const now = Date.parse('2026-01-07T00:00:00Z');
  assert.equal(canShowWorkspaceFacts(fact, now), true);
  assert.equal(canShowWorkspaceFacts(fact, Date.parse(fact.expiresAt)), false);
  for (const status of ['pending', 'partial', 'failed', 'permission_denied', 'stale']) {
    assert.equal(canShowWorkspaceFacts({ ...fact, status }, now), false, status);
  }
  for (const permission of ['read', 'denied', 'unknown']) {
    assert.equal(canShowWorkspaceFacts({ ...fact, permission }, now), false, permission);
  }
  assert.equal(canShowWorkspaceFacts({ ...fact, accessStatus: 'permission_denied' }, now), false);
  assert.equal(canShowWorkspaceFacts({ ...fact, completeness: 'partial' }, now), false);
  assert.equal(canShowWorkspaceFacts({ ...fact, expiresAt: undefined }, now), false);
  assert.equal(canShowWorkspaceFacts(null, now), false);
});
