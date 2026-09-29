import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createDestinationStore, DestinationError } from '../src/rebuild/owner/deliveryDestination.ts';

const validInput = { name: '模拟渠道', endpoint: 'https://delivery.example.test/api', targetGroup: 'team-seatwatch', secret: 'channel-secret' };

test('destination persistence redacts secret and selection needs fresh successful tests', async () => {
  let seenSecret = '';
  const store = createDestinationStore(async (input) => {
    seenSecret = input.secret;
    return { connection: 'connected', target: 'connected' };
  });
  const created = store.create(true, validInput);
  assert.equal(created.hasSecret, true);
  assert.equal('secret' in created, false);
  assert.throws(() => store.select(true, created.id), (error) => error instanceof DestinationError && error.code === 'not_selectable');

  const tested = await store.test(true, created.id);
  assert.equal(tested.test?.connection, 'connected');
  assert.equal(tested.test?.target, 'connected');
  assert.equal(seenSecret, 'channel-secret');
  const selected = store.select(true, created.id);
  assert.equal(selected.id, created.id);
  assert.equal(store.selected(true), created.id);
  assert.equal(store.list(true)[0].hasSecret, true);
  assert.throws(() => store.create(true, { ...validInput, endpoint: 'not-https' }), (error) => {
    assert.equal(error.code, 'invalid_destination');
    assert.equal(String(error).includes('channel-secret'), false);
    return true;
  });
});

test('connection failure and target mismatch remain actionable and cannot select', async () => {
  const store = createDestinationStore(async () => ({ connection: 'connection_failed', target: 'untested' }));
  const created = store.create(true, validInput);
  const result = await store.test(true, created.id);
  assert.deepEqual(result.test && { connection: result.test.connection, target: result.test.target }, { connection: 'connection_failed', target: 'untested' });
  assert.throws(() => store.select(true, created.id), (error) => error.code === 'not_selectable');

  const deniedStore = createDestinationStore(async () => ({ connection: 'permission_denied', target: 'permission_denied' }));
  const denied = deniedStore.create(true, validInput);
  const deniedResult = await deniedStore.test(true, denied.id);
  assert.equal(deniedResult.test?.connection, 'permission_denied');
  assert.equal(deniedResult.test?.target, 'permission_denied');

  const mismatchStore = createDestinationStore(async () => ({ connection: 'connected', target: 'target_mismatch' }));
  const mismatch = mismatchStore.create(true, validInput);
  const mismatchResult = await mismatchStore.test(true, mismatch.id);
  assert.equal(mismatchResult.test?.target, 'target_mismatch');
  assert.throws(() => mismatchStore.select(true, mismatch.id), (error) => error.code === 'not_selectable');
});

test('updates invalidate an old test and unauthorized operations are rejected', async () => {
  const store = createDestinationStore(async () => ({ connection: 'connected', target: 'connected' }));
  const created = store.create(true, validInput);
  await store.test(true, created.id);
  const updated = store.update(true, created.id, { ...validInput, name: '修改后的渠道', secret: '' });
  assert.equal(updated.name, '修改后的渠道');
  assert.equal(updated.test, null);
  assert.equal(updated.hasSecret, true);
  assert.throws(() => store.list(false), (error) => error.code === 'permission_denied');
  assert.throws(() => store.create(false, validInput), (error) => error.code === 'permission_denied');
});
