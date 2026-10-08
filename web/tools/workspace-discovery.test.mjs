import test from 'node:test';
import assert from 'node:assert/strict';
import { canDiscover, confirmWorkspace, isWorkspaceSelectable } from '../src/owner/workspaceSelection.ts';

const visible = { id: 'team-1', displayName: 'One', accessStatus: 'readable' };
const denied = { id: 'team-2', displayName: 'Two', accessStatus: 'permission_denied' };
const unknown = { id: 'team-3', displayName: 'Three', accessStatus: 'unknown' };

test('single workspace needs an explicit choice; visibility is not management permission', () => {
  const result = { status: 'discovered', workspaces: [visible] };
  assert.equal(confirmWorkspace(result, ''), null);
  assert.equal(confirmWorkspace(result, visible.id), visible.id);
  assert.equal(confirmWorkspace(result, 'typed-technical-id'), null);
});

test('multiple mothers can show one real workspace; only readable candidates are selectable', () => {
  const result = { status: 'discovered', workspaces: [visible, denied, unknown] };
  assert.equal(isWorkspaceSelectable(visible), true);
  assert.equal(isWorkspaceSelectable(denied), false);
  assert.equal(isWorkspaceSelectable(unknown), false);
  assert.equal(confirmWorkspace(result, denied.id), null);
  assert.equal(confirmWorkspace(result, unknown.id), null);
  for (const status of ['not_verified', 'discovering', 'empty', 'session_expired', 'missing_credentials', 'discovery_failed', 'permission_denied', 'unavailable']) {
    assert.equal(confirmWorkspace({ ...result, status }, visible.id), null, status);
  }
});

test('discovery stays locked until explicit Personal verification succeeds', () => {
  assert.equal(canDiscover(null), false);
  for (const status of ['not_verified', 'verifying', 'invalid_login', 'missing_credentials', 'refresh_failed', 'unavailable']) {
    assert.equal(canDiscover({ status }), false, status);
  }
  assert.equal(canDiscover({ status: 'ready' }), true);
});
