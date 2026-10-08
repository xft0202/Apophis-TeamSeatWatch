import test from 'node:test';
import assert from 'node:assert/strict';
import { operationActionFailure, operationProgress, operationSteps, invitationBlockers } from '../src/owner/operationWizardState.ts';
import { operationRecordStatus } from '../src/owner/operationRecordStatus.ts';

const batch = { id: 'run', targetCount: 125 };
const invitation = { status: 'awaiting_login', targetTotal: 125, invitationConfirmedCount: 125, succeededCount: 0 };
const joined = { ...invitation, status: 'succeeded', succeededCount: 125 };

test('full and partial capacity expose one concise blocker without claiming OAuth eligibility', () => {
  const full = { blockers: [{ code: 'premium_capacity_exceeded', message: 'redundant capacity counters and OAuth advice' }], availablePremiumSeats: 0, waitingSeatCount: 1 };
  assert.deepEqual(invitationBlockers(full), [{ code: 'premium_capacity_exceeded', message: '高级席位已满，1 个账号等待席位' }]);
  assert.deepEqual(invitationBlockers({ blockers: [], availablePremiumSeats: 9, waitingSeatCount: 11 }), [{ code: 'premium_capacity_exceeded', message: '高级席位不足，11 个账号等待席位' }]);
  const conflict = { code: 'join_conflict', message: '所选账号正在其他操作中' };
  assert.deepEqual(invitationBlockers({ ...full, blockers: [conflict, ...full.blockers, ...full.blockers] }), [conflict, { code: 'premium_capacity_exceeded', message: '高级席位已满，1 个账号等待席位' }]);
  assert.deepEqual(invitationBlockers({ blockers: [], availablePremiumSeats: 1, waitingSeatCount: 0 }), []);
  assert.deepEqual(invitationBlockers(null), []);
});
const logins = { total: 125, readyCount: 125, cardCount: 0, items: [] };

test('whole-scope results advance five stages independently of the visible account page', () => {
  assert.deepEqual(operationSteps, ['选择对象', '发送邀请', 'OAuth 登录', '生成卡密', '本轮结果']);
  assert.equal(operationProgress(null, null, null).frontier, 0);
  assert.equal(operationProgress(batch, null, null).frontier, 1);
  assert.equal(operationProgress(batch, joined, { ...logins, readyCount: 124 }).frontier, 3);
  assert.equal(operationProgress(batch, joined, logins).frontier, 3);
  assert.equal(operationProgress(batch, joined, { ...logins, cardCount: 125, items: [] }).frontier, 4);
});

test('confirmed invitations unlock step 3 without pretending members have joined', () => {
  assert.equal(operationProgress(batch, invitation, null).frontier, 2);
  assert.equal(operationProgress(batch, { ...invitation, invitationConfirmedCount: 124 }, logins).allInvited, false);
  assert.equal(operationProgress(batch, { ...invitation, targetTotal: 124 }, logins).allInvited, false);
  assert.equal(operationProgress(batch, { ...invitation, status: 'blocked', succeededCount: 2 }, null).invited, true);
});

test('an active original operation reports the conflict rather than a generic state change', () => {
  assert.match(operationActionFailure({ status: 409, code: 'join_conflict' }, 'failed'), /所选账号.*重复执行.*对应操作/);
  assert.match(operationActionFailure({ status: 409, code: 'invitation_incomplete' }, 'failed'), /尚无已确认邀请/);
  assert.equal(operationActionFailure({ status: 409, code: 'unknown' }, '明确的阶段错误'), '明确的阶段错误');
});

test('one successful visible page never implies all accounts are logged in', () => {
  const page = { ...logins, total: 125, readyCount: 20, items: Array.from({ length: 20 }, () => ({ status: 'ready' })) };
  assert.equal(operationProgress(batch, joined, page).loggedIn, false);
  assert.equal(operationProgress(batch, joined, { ...logins, total: 124 }).loggedIn, false);
});

test('saved cards and login completion are distinct prerequisites', () => {
  assert.equal(operationProgress(batch, joined, { ...logins, cardCount: 124 }).cardsSaved, false);
  assert.equal(operationProgress(batch, joined, { ...logins, readyCount: 124, cardCount: 125 }).cardsSaved, false);
});

test('credentials alone cannot pretend an unfinished membership operation succeeded', () => {
  assert.equal(operationProgress(batch, invitation, logins).loggedIn, false);
  assert.equal(operationProgress(batch, invitation, logins).canGenerateCards, false);
});

test('one OAuth success unlocks cards and saved cards unlock results without hiding unfinished accounts', () => {
  const scope = { ...batch, targetCount: 3 };
  const partial = { ...invitation, status: 'blocked', targetTotal: 3, invitationConfirmedCount: 3, succeededCount: 1, blockedCount: 2 };
  const ready = { ...logins, total: 1, readyCount: 1 };
  const progress = operationProgress(scope, partial, ready);
  assert.equal(progress.canGenerateCards, true);
  assert.equal(progress.loggedIn, false);
  assert.equal(progress.frontier, 3);
  const saved = operationProgress(scope, partial, { ...ready, cardCount: 1 });
  assert.equal(saved.frontier, 4);
  assert.equal(saved.cardsSaved, false);
  const retried = operationProgress(scope, { ...partial, succeededCount: 2 }, { ...ready, total: 2, readyCount: 2, cardCount: 1 });
  assert.equal(retried.canGenerateCards, true);
  assert.equal(retried.frontier, 4);
  assert.equal(retried.cardsSaved, false);
});

test('zero successful OAuth accounts cannot generate cards; existing results remain viewable', () => {
  assert.equal(operationProgress(batch, invitation, { ...logins, readyCount: 0 }).canGenerateCards, false);
  assert.equal(operationProgress(batch, invitation, { ...logins, readyCount: 0 }).frontier, 2);
  assert.equal(operationProgress(batch, joined, { ...logins, readyCount: 0, cardCount: 1 }).frontier, 4);
});

test('stopped invitations do not look running and partial OAuth/cards do not look fully complete', () => {
  const scope = { status: 'joining', targetCount: 3, execution: { activeTaskCount: 0, invitationConfirmedCount: 0, invitationFailedCount: 3, invitationUncertainCount: 0, readyCount: 0, cardCount: 0 } };
  assert.equal(operationRecordStatus(scope).label, '邀请未完成');
  assert.equal(operationRecordStatus({ ...scope, execution: { ...scope.execution, invitationFailedCount: 0, invitationUncertainCount: 1 } }).label, '邀请待核验');
  assert.equal(operationRecordStatus({ ...scope, execution: { ...scope.execution, activeTaskCount: 1 } }).label, '邀请处理中');
  const invited = { ...scope, loginStartedAt: '2026-10-07T00:00:00Z', status: 'serving', execution: { ...scope.execution, invitationConfirmedCount: 3, invitationFailedCount: 0 } };
  assert.equal(operationRecordStatus(invited).label, 'OAuth 未完成');
  assert.equal(operationRecordStatus({ ...invited, execution: { ...invited.execution, readyCount: 1 } }).label, 'OAuth 部分成功');
  assert.equal(operationRecordStatus({ ...invited, execution: { ...invited.execution, readyCount: 1, cardCount: 1 } }).label, '部分完成');
  assert.equal(operationRecordStatus({ ...invited, execution: { ...invited.execution, readyCount: 3 } }).label, '待生成卡密');
  assert.equal(operationRecordStatus({ ...invited, execution: { ...invited.execution, readyCount: 3, cardCount: 3 } }).label, '已完成');
});


test('a twenty-account batch can advance with nine invitations while eleven wait for seats', () => {
  const batch = { targetCount: 20 };
  const invitations = { targetTotal: 20, invitationConfirmedCount: 9, succeededCount: 0, waitingSeatCount: 11 };
  const progress = operationProgress(batch, invitations, null);
  assert.equal(progress.invited, true);
  assert.equal(progress.allInvited, false);
  assert.equal(progress.frontier, 2);
  assert.equal(operationProgress(batch, { ...invitations, invitationConfirmedCount: 0 }, null).frontier, 1);
  const ready = { total: 1, readyCount: 1, cardCount: 0 };
  assert.equal(operationProgress(batch, { ...invitations, succeededCount: 1 }, ready).frontier, 3);
  assert.equal(operationProgress(batch, { ...invitations, succeededCount: 1 }, { ...ready, cardCount: 1 }).frontier, 4);
  const execution = { activeTaskCount: 0, invitationConfirmedCount: 9, invitationWaitingSeatCount: 11, invitationFailedCount: 0, invitationUncertainCount: 0, readyCount: 0, cardCount: 0 };
  assert.equal(operationRecordStatus({ ...batch, execution }).label, '邀请部分完成');
  assert.equal(operationRecordStatus({ ...batch, execution: { ...execution, invitationConfirmedCount: 0 } }).label, '等待席位');
  assert.equal(operationRecordStatus({ ...batch, execution: { ...execution, readyCount: 1, cardCount: 1 } }).label, '部分完成');
});
