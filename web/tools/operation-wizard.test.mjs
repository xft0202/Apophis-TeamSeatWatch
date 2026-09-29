import test from 'node:test';
import assert from 'node:assert/strict';
import { canChooseDestination, draftStatus, wizardStep } from '../src/rebuild/owner/operationWizardState.ts';

const clone = (value) => JSON.parse(JSON.stringify(value));

function draft(overrides = {}) {
  return {
    id: 'draft-1', version: 1, step: 'mother', children: [],
    motherCurrent: false, workspaceCurrent: false, batchCurrent: false, destinationCurrent: false,
    updatedAt: '2026-01-01T00:00:00Z', ...overrides,
  };
}

// Mock the selection contract, not a platform API. There is deliberately no execute method.
function mockedSelections() {
  let saved = null;
  return {
    async get() { if (!saved) throw Object.assign(new Error('empty'), { status: 404 }); return clone(saved); },
    async start() { saved ??= draft(); return clone(saved); },
    async change(change) {
      if (change.expectedVersion !== saved.version) throw Object.assign(new Error('stale'), { status: 409 });
      saved.version++;
      if (change.choice === 'back') saved.step = change.backTo;
      if (change.choice === 'mother') {
        Object.assign(saved, { step: 'workspace', motherAccountId: change.motherAccountId, motherRevision: 1, motherCurrent: true, workspaceId: undefined, batchId: undefined, destinationId: undefined, children: [], workspaceCurrent: false, batchCurrent: false, destinationCurrent: false });
      }
      if (change.choice === 'workspace') Object.assign(saved, { step: 'children', workspaceId: change.workspaceId, workspaceCurrent: true, batchId: undefined, destinationId: undefined, children: [], batchCurrent: false, destinationCurrent: false });
      if (change.choice === 'children') Object.assign(saved, { step: 'destination', batchId: change.batchId, children: clone(change.children), batchCurrent: true, destinationId: undefined, destinationCurrent: false });
      if (change.choice === 'destination') Object.assign(saved, { step: 'complete', destinationId: change.destinationId, destinationCurrent: true });
      return clone(saved);
    },
  };
}

test('empty installation has an explicit start; a sole workspace is never auto-confirmed', async () => {
  const api = mockedSelections();
  await assert.rejects(api.get(), { status: 404 });
  assert.equal(wizardStep(null), 'start');
  let current = await api.start();
  assert.equal(wizardStep(current), 'mother');
  current = await api.change({ expectedVersion: current.version, choice: 'mother', motherAccountId: 'mother-1' });
  assert.equal(current.step, 'workspace');
  assert.equal(current.workspaceId, undefined);
  assert.equal(canChooseDestination(current), false);
});

test('mocked backtracking and restart preserve exact children until a changed mother clears downstream', async () => {
  const api = mockedSelections();
  let current = await api.start();
  current = await api.change({ expectedVersion: current.version, choice: 'mother', motherAccountId: 'mother-1' });
  current = await api.change({ expectedVersion: current.version, choice: 'workspace', workspaceId: 'canonical-workspace' });
  const exact = [{ accountId: 'child-1', membershipVersion: 7 }];
  current = await api.change({ expectedVersion: current.version, choice: 'children', batchId: 'batch-1', children: exact });
  assert.equal(canChooseDestination(current), true);
  current = await api.change({ expectedVersion: current.version, choice: 'destination', destinationId: 'tested-destination' });
  assert.deepEqual((await api.get()).children, exact, 'refresh/restart does not re-read widened batch');
  current = await api.change({ expectedVersion: current.version, choice: 'back', backTo: 'workspace' });
  assert.equal((await api.get()).step, 'workspace', 'repair returns to exact prior step');
  await assert.rejects(api.change({ expectedVersion: 1, choice: 'workspace', workspaceId: 'other' }), { status: 409 });
  current = await api.change({ expectedVersion: current.version, choice: 'mother', motherAccountId: 'mother-2' });
  assert.deepEqual(current.children, []);
  assert.equal(current.destinationId, undefined);
  assert.equal(current.workspaceId, undefined);
});

test('errors and stale evidence block progress without guessing eligible seats or write permission', () => {
  const selected = draft({ step: 'complete', motherAccountId: 'm', workspaceId: 'w', batchId: 'b', destinationId: 'd', motherCurrent: true, workspaceCurrent: true, batchCurrent: true, destinationCurrent: true, children: [{ accountId: 'a', membershipVersion: 4 }] });
  assert.equal(draftStatus(selected), '');
  assert.match(draftStatus({ ...selected, motherCurrent: false }), /母号/);
  assert.match(draftStatus({ ...selected, workspaceCurrent: false }), /空间/);
  assert.match(draftStatus({ ...selected, batchCurrent: false }), /批次/);
  assert.match(draftStatus({ ...selected, destinationCurrent: false }), /去向/);
  assert.equal(canChooseDestination({ ...selected, workspaceCurrent: false }), false);
  assert.equal(canChooseDestination({ ...selected, batchCurrent: false }), false);
});
