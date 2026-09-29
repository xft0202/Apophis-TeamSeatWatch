import test from 'node:test';
import assert from 'node:assert/strict';
import { canConfirmRotation, explicitRotationAssignments, rotationStatus } from '../src/rebuild/owner/rotationPreviewState.ts';

const now = Date.parse('2026-01-01T00:00:00Z');
const ready = {
  status: 'ready', authorized: false, draftVersion: 4, expiresAt: '2026-01-01T00:01:00Z',
  slots: [{ decision: 'replaceable' }, { decision: 'retained' }],
  candidates: [{ decision: 'eligible' }, { decision: 'excluded', reason: 'invitation_required' }],
};
test('wizard continuation confirms only a fresh, exact ready snapshot', () => {
  assert.equal(canConfirmRotation(ready, 4, now), true);
  assert.equal(canConfirmRotation({ ...ready, status: 'pending_permission' }, 4, now), false);
  assert.equal(canConfirmRotation({ ...ready, status: 'facts_incomplete' }, 4, now), false);
  assert.equal(canConfirmRotation({ ...ready, status: 'not_expired' }, 4, now), false);
  assert.equal(canConfirmRotation({ ...ready, status: 'needs_verification' }, 4, now), false);
  assert.equal(canConfirmRotation({ ...ready, status: 'revoked' }, 4, now), false);
  assert.equal(canConfirmRotation({ ...ready, authorized: true }, 4, now), false);
  assert.equal(canConfirmRotation(ready, 5, now), false);
  assert.equal(canConfirmRotation(ready, 4, Date.parse(ready.expiresAt)), false);
  assert.equal(canConfirmRotation({ ...ready, candidates: [{ decision: 'excluded' }] }, 4, now), false);
  assert.equal(canConfirmRotation({ ...ready, slots: [{ decision: 'retained' }] }, 4, now), false);
  assert.match(rotationStatus({ ...ready, status: 'pending_permission' }), /可读取空间不代表允许管理/);
  assert.match(rotationStatus({ ...ready, status: 'needs_verification' }), /不会整批清退/);
});

test('Owner must explicitly match each independently eligible candidate to one compatible original seat', () => {
  const review = {
    ...ready,
    slots: [{ platformMemberId: 'slot-a', seatType: 'default', decision: 'replaceable' }, { platformMemberId: 'slot-b', seatType: 'default', decision: 'replaceable' }],
    candidates: [{ accountId: 'child-a', seatType: 'default', decision: 'eligible' }, { accountId: 'child-b', seatType: 'default', decision: 'eligible' }],
  };
  assert.equal(explicitRotationAssignments(review, {}), null);
  assert.equal(explicitRotationAssignments(review, { 'slot-a': 'child-a' }), null);
  assert.equal(explicitRotationAssignments(review, { 'slot-a': 'child-a', 'slot-b': 'child-a' }), null);
  assert.equal(explicitRotationAssignments(review, { 'slot-a': 'child-a', 'slot-b': 'child-b', extra: 'child-a' }), null);
  assert.deepEqual(explicitRotationAssignments(review, { 'slot-a': 'child-b', 'slot-b': 'child-a' }), [
    { platformMemberId: 'slot-a', accountId: 'child-b' }, { platformMemberId: 'slot-b', accountId: 'child-a' },
  ]);
  assert.equal(explicitRotationAssignments({ ...review, candidates: [...review.candidates, { accountId: 'child-c', seatType: 'default', decision: 'eligible' }] }, { 'slot-a': 'child-a', 'slot-b': 'child-b' }), null);
  assert.equal(explicitRotationAssignments({ ...review, candidates: [{ ...review.candidates[0], seatType: 'usage_based' }, review.candidates[1]] }, { 'slot-a': 'child-a', 'slot-b': 'child-b' }), null);
});
