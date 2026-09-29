import test from 'node:test';
import assert from 'node:assert/strict';
import { canShowWorkspaceFacts, createWorkspaceRequestGate } from '../src/rebuild/owner/workspaceVerification.ts';

const fact = {
  status: 'verified', permission: 'manage', accessStatus: 'readable',
  completeness: 'complete', expiresAt: '2026-01-08T00:00:00Z',
};

test('verified facts need current, complete read evidence', () => {
  const now = Date.parse('2026-01-07T00:00:00Z');
  assert.equal(canShowWorkspaceFacts(fact, now), true);
  assert.equal(canShowWorkspaceFacts(fact, Date.parse(fact.expiresAt)), false);
  for (const status of ['pending', 'partial', 'failed', 'permission_denied', 'stale']) {
    assert.equal(canShowWorkspaceFacts({ ...fact, status }, now), false, status);
  }
  assert.equal(canShowWorkspaceFacts({ ...fact, permission: 'read' }, now), true, 'read success proves facts, not write authority');
  for (const permission of ['denied', 'unknown']) {
    assert.equal(canShowWorkspaceFacts({ ...fact, permission }, now), false, permission);
  }
  assert.equal(canShowWorkspaceFacts({ ...fact, accessStatus: 'permission_denied' }, now), false);
  assert.equal(canShowWorkspaceFacts({ ...fact, completeness: 'partial' }, now), false);
  assert.equal(canShowWorkspaceFacts({ ...fact, expiresAt: undefined }, now), false);
  assert.equal(canShowWorkspaceFacts(null, now), false);
  assert.equal(canShowWorkspaceFacts({ ...fact, status: 'verifying' }, now), false);
});

test('old poll cannot restore prior success over pending or failed POST', () => {
  const gate = createWorkspaceRequestGate();
  const oldPoll = gate.beginPoll();
  assert.notEqual(oldPoll, null);
  const mutation = gate.beginMutation();
  assert.equal(gate.beginPoll(), null);
  assert.equal(gate.acceptPoll(oldPoll), false);
  assert.equal(gate.acceptMutation(mutation), true);
  assert.equal(gate.acceptPoll(oldPoll), false);
  const nextPoll = gate.beginPoll();
  assert.notEqual(nextPoll, null);
  gate.invalidate();
  assert.equal(gate.acceptPoll(nextPoll), false);
  assert.equal(gate.acceptMutation(mutation), false);
});

test('older POST completion cannot end or reveal a newer in-flight verification', () => {
  const gate = createWorkspaceRequestGate();
  const older = gate.beginMutation();
  const newer = gate.beginMutation();
  assert.equal(gate.isCurrentMutation(older), false);
  assert.equal(gate.acceptMutation(older), false);
  assert.equal(gate.beginPoll(), null);
  assert.equal(gate.isCurrentMutation(newer), true);
  assert.equal(gate.acceptMutation(newer), true);
});
