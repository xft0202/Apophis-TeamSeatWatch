import test from 'node:test';
import assert from 'node:assert/strict';
import { seatTypeLabel, workspaceSeatSummary } from '../src/owner/seatTypes.ts';

test('seat display distinguishes entitlement, occupied members and pending invitations', () => {
  const result = workspaceSeatSummary({ seatLimit: 2, memberCount: 11, pendingInviteCount: 99, seatTypeCounts: { default: 2, prolite: 9 }, pendingInviteSeatTypeCounts: { prolite: 99 } });
  assert.deepEqual(result, [
    { type: 'default', label: '普通席位', opened: undefined, members: 2, invitations: 0, remaining: undefined },
    { type: 'prolite', label: '高级席位', opened: undefined, members: 9, invitations: 99, remaining: undefined },
  ]);
});

test('missing seat evidence stays unknown and other observed classes remain visible', () => {
  for (const row of workspaceSeatSummary(null)) { assert.equal(row.members, undefined); assert.equal(row.invitations, undefined); }
  assert.equal(seatTypeLabel(), '未确认');
  assert.equal(seatTypeLabel('unexpected'), '未确认');
  const result = workspaceSeatSummary({ seatTypeCounts: { usage_based: 1, unknown: 2 }, pendingInviteSeatTypeCounts: { automation: 1 } });
  assert.equal(result.length, 5);
  assert.equal(result.find((row) => row.type === 'unknown').members, 2);
});
