import test from 'node:test';
import assert from 'node:assert/strict';
import { workspaceSeatSummary } from '../src/owner/seatTypes.ts';

test('nine opened premium seats occupied by nine members leave zero invitations', () => {
  const premium = workspaceSeatSummary({ seatEntitlements: { default: 2, prolite: 9 }, seatTypeCounts: { default: 2, prolite: 9 }, pendingInviteSeatTypeCounts: { prolite: 101 } }).find(row => row.type === 'prolite');
  assert.equal(premium.opened, 9);
  assert.equal(premium.members, 9);
  assert.equal(premium.remaining, 0);
});

test('pending invitations reserve the unoccupied premium seats', () => {
  const premium = workspaceSeatSummary({ seatEntitlements: { prolite: 9 }, seatTypeCounts: { prolite: 8 }, pendingInviteSeatTypeCounts: { prolite: 1 } }).find(row => row.type === 'prolite');
  assert.equal(premium.remaining, 0);
  assert.equal(workspaceSeatSummary({ seatEntitlements: { prolite: 9 }, seatTypeCounts: { prolite: 8 }, pendingInviteSeatTypeCounts: { prolite: 0 } }).find(row => row.type === 'prolite').remaining, 1);
});

test('missing opened, occupied or pending evidence never invents free seats', () => {
  for (const missing of ['seatEntitlements', 'seatTypeCounts', 'pendingInviteSeatTypeCounts']) {
    const facts = { seatEntitlements: { prolite: 9 }, seatTypeCounts: { prolite: 0 }, pendingInviteSeatTypeCounts: { prolite: 0 } };
    delete facts[missing];
    assert.equal(workspaceSeatSummary(facts).find(row => row.type === 'prolite').remaining, undefined);
  }
});
