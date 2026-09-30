import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { removalSlotAction, removalSlotStatus, removalErrorMessage } from '../src/rebuild/owner/rotationRemovalState.ts';

function slot(state, more = {}) {
  return { id: 'slot', platformMemberId: 'frozen-member', originalAccountId: 'original', candidateAccountId: 'frozen-candidate', identifier: 'original@example.test', seatType: 'prolite', state, attemptCount: 0, leaseEpoch: 0, uncertainObligation: false, lastErrorCode: '', updatedAt: '2026-01-01T00:00:00Z', candidateReady: false, ...more };
}
const progress = { previewId: 'original-preview', workspaceId: 'selected-space', workspaceName: 'selected', createdAt: '2026-01-01T00:00:00Z', authorizationDigest: 'a'.repeat(64), stopped: false, writeAllowed: true, slots: [] };

test('all eight persisted states have unambiguous owner-facing labels; only committed readiness says empty', () => {
  for (const state of ['pending', 'lease_acquired', 'remove_requested', 'remote_result_uncertain', 'absent_verification_pending', 'absent_verified', 'blocked', 'stopped']) {
    assert.equal(typeof removalSlotStatus(slot(state)), 'string');
    assert.notEqual(removalSlotStatus(slot(state)), state);
    if (state !== 'absent_verified') assert.doesNotMatch(removalSlotStatus(slot(state)), /空位已核实/);
  }
  assert.equal(removalSlotStatus(slot('absent_verified')), '空位证据待更新');
  assert.equal(removalSlotStatus(slot('absent_verified', { candidateReady: true })), '空位已核实');
});
test('no receipt, timeout, restart or different UI state can cause a second DELETE', () => {
  for (const state of ['remove_requested', 'remote_result_uncertain', 'absent_verification_pending', 'blocked', 'stopped', 'absent_verified']) {
    assert.equal(removalSlotAction(progress, slot(state, { remoteRequestId: 'original-request' }), 0), 'verify');
  }
  assert.equal(removalSlotAction(progress, slot('pending'), 0), 'run');
  assert.equal(removalSlotAction(progress, slot('blocked'), 0), 'run');
});
test('stop, authorization drift and live leases prevent new dispatch but preserve read-only reconciliation', () => {
  for (const removal of [{ ...progress, stopped: true }, { ...progress, writeAllowed: false }]) {
    assert.equal(removalSlotAction(removal, slot('pending'), 0), null);
    assert.equal(removalSlotAction(removal, slot('stopped', { remoteRequestId: 'original-request' }), 0), 'verify');
  }
  const leased = slot('lease_acquired', { leaseExpiresAt: '2026-01-01T00:01:00Z' });
  assert.equal(removalSlotAction(progress, leased, Date.parse('2026-01-01T00:00:00Z')), null);
  assert.equal(removalSlotAction(progress, leased, Date.parse('2026-01-01T00:02:00Z')), 'run');
});
test('browser mount, refresh and history recovery use only reads; stopped work is not silently recreated', () => {
  const ui = readFileSync(new URL('../src/rebuild/owner/RotationRemovalPanel.tsx', import.meta.url), 'utf8');
  const effects = ui.slice(ui.indexOf('  useEffect('), ui.indexOf('  async function control'));
  assert.doesNotMatch(effects, /rotationRemovalApi\.(start|run|stop|verify)\(/);
  assert.match(ui, /rotationRemovalApi\.history/);
  assert.match(ui, /expiryRotationApi\.get\(id\)/);
  assert.match(ui, /rotationRemovalApi\.get\(id\)/);
  assert.match(ui, /停止后续清退/);
  assert.match(ui, /只读核实原槽/);
  assert.doesNotMatch(ui, /Drawer|<input|<button|<select|重新执行整批/);
  assert.match(ui, /slot\.candidateReady \? '空位已核实，候选加入尚未启用' : '不可加入'/);
});
test('removal panel uses short business states and actions rather than teaching paragraphs', () => {
  const ui = readFileSync(new URL('../src/rebuild/owner/RotationRemovalPanel.tsx', import.meta.url), 'utf8');
  assert.match(ui, /停止后续清退/);
  assert.match(ui, /只读核实原槽/);
  assert.doesNotMatch(ui, /回执不算空位|停止或撤权不会撤回/);
});
test('request keys and target scope are stable after a lost response, reload or new login', () => {
  const client = readFileSync(new URL('../src/rebuild/owner/rotationRemoval.ts', import.meta.url), 'utf8');
  assert.match(client, /const digest = preview\.authorizationDigest/);
  assert.match(client, /if \(!digest\) throw/);
  assert.match(client, /authorizationDigest: digest/);
  assert.match(client, /idempotencyKey: preview\.id/);
  assert.doesNotMatch(client, /randomUUID|assignments:|platformMemberId:/);
  assert.match(client, /path: \{ previewId, slotId \}/);
  assert.doesNotMatch(client, /\.DELETE\(/);
});
test('conflicts and transport errors do not claim completion or expose secret/technical response bodies', () => {
  assert.match(removalErrorMessage(409), /不要重建目标或重发清退/);
  assert.match(removalErrorMessage(401), /重新登录/);
  assert.match(removalErrorMessage(500), /仍按占用或待核验保留/);
});
