import { test } from 'node:test';
import assert from 'node:assert/strict';
import { personalProbeScope } from '../src/rebuild/owner/personalProbeScope.ts';

test('personal probe selected IDs take precedence across pages and exclude the search filter', () => {
  const selected = new Set(['page-2', 'page-1']);
  assert.deepEqual(personalProbeScope(selected, 'ignored-filter'), { targetAccountIds: ['page-1', 'page-2'] });
  assert.deepEqual(personalProbeScope(new Set(), 'all-matches'), { search: 'all-matches' });
});
