import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
import { URL, fileURLToPath } from 'node:url';

const contract = await readFile(fileURLToPath(new URL('../../api/openapi/owner.yaml', import.meta.url)), 'utf8');
const adapter = await readFile(fileURLToPath(new URL('../src/owner/deliveryDestination.ts', import.meta.url)), 'utf8');

test('delivery destination contract is Owner-authenticated and secret-redacting', () => {
  assert.match(contract, /\/api\/owner\/v1\/delivery-destinations:/);
  assert.match(contract, /operationId: testDeliveryDestination/);
  assert.match(contract, /operationId: selectDeliveryDestination/);
  assert.match(adapter, /credentials: 'include'/);
  assert.match(contract, /hasSecret/);
  assert.match(adapter, /components\['schemas'\]\['DeliveryDestination'\]/);
  assert.doesNotMatch(adapter, /createDestinationStore|destinationStore/);
});

test('simulated remote outcome vocabulary remains actionable', () => {
  for (const outcome of ['connection_failed', 'permission_denied', 'target_mismatch', 'untested']) {
    assert.match(contract, new RegExp(outcome));
  }
  assert.match(adapter, /not_selectable/);
});
