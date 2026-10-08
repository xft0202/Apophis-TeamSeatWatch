// Exercise the real frontend with isolated API fixtures and a controlled clock.
import { chromium } from '/root/.local/share/playwright-cli/node_modules/playwright/index.mjs';
import assert from 'node:assert/strict';
import { test } from 'node:test';

const ownerURL = process.env.TSW_OWNER_BROWSER_URL ?? 'http://127.0.0.1:5192';
const publicURL = process.env.TSW_PUBLIC_BROWSER_URL ?? 'http://127.0.0.1:5193';
const launch = () => chromium.launch({ executablePath: process.env.TSW_CHROMIUM_PATH ?? '/root/.cache/ms-playwright/chromium-1243/chrome-linux64/chrome', headless: true, args: ['--no-sandbox'] });
const future = '2027-10-07T00:00:00Z';
const mother = '10000000-0000-4000-8000-000000000001';
const workspace = '20000000-0000-4000-8000-000000000001';
const batchId = '30000000-0000-4000-8000-000000000001';
const accountId = '40000000-0000-4000-8000-000000000001';
const sourceId = '50000000-0000-4000-8000-000000000001';
const account = { id: accountId, identifier: 'account@example.test', status: 'active', materialStatus: 'complete', hasPassword: true, hasTotp: true, hasRecovery: false, secretRevision: 1, version: 1, updatedAt: '2026-10-07T00:00:00Z' };
const batch = { id: batchId, motherAccountId: mother, motherAccountName: 'mother@example.test', workspaceId: workspace, workspaceName: '测试空间', sourceBatchId: sourceId, sourceBatchName: '测试批次', targetCount: 1, sequenceNo: 1, plannedAt: future, status: 'joining' };

async function fixture(browser, { partial = false } = {}) {
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  await page.clock.install({ time: new Date('2026-10-07T00:00:00Z') });
  await page.clock.pauseAt(new Date('2026-10-07T00:00:01Z'));
  const errors = [], unexpectedWrites = [];
  page.on('pageerror', error => errors.push(error.message));
  const config = { kind: 'cliproxy', host: 'proxy.example.test', port: 443, protocol: 'socks5h', country: 'US', sessionType: 'sticky', sessionMinutes: 120 };
  const source = { kind: 'cliproxy', enabled: true, config, usernameSet: true, passwordSet: true };
  const pool = { mode: 'proxy_required', source, sources: [source], providers: [{ kind: 'cliproxy', host: config.host, minMinutes: 3, maxMinutes: 120 }], nodes: [1, 2, 3].map(index => ({ id: `proxy-${index}`, scheme: 'socks5h', displayHost: 'proxy.example.test:443', sourceKind: 'cliproxy', sessionKey: `session-${index}`, region: 'US', stableUntil: future, state: 'healthy', failureReason: '', leaseCount: 0, diagnostics: [] })), healthyCount: 3, pendingCount: 0, isolatedCount: 0, activeLeases: 0, targetHealthy: 3, probeConcurrency: 3, taskConcurrency: 3, total: 3, page: 1, pageSize: 20, lastRefillAt: future, refillRequested: 1, refillGenerated: 1, refillSucceeded: 1, refilling: false };
  const state = { settingsFailure: false, accountReadFailure: false };
  const operationBatch = { ...batch, targetCount: partial ? 20 : 1 };
  const preview = { batch: operationBatch, targetSeatType: 'prolite', paidPremiumSeats: 9, occupiedPremiumSeats: 9, reservedPremiumSeats: 102, availablePremiumSeats: 0, newInvitationCount: partial ? 11 : 1, plannedInvitationCount: 0, waitingSeatCount: partial ? 11 : 1, canProceed: false, blockers: [{ code: 'premium_capacity_exceeded', message: '高级席位已开通 9 个、已占用 9 个、待接受或处理中 102 个，剩余可邀请 0 个；本次 1 个账号等待席位，已有已确认账号可继续处理' }] };
  await page.route('**/api/owner/**', async route => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname;
    const json = (body, status = 200) => route.fulfill({ status, json: body });
    if (path.endsWith('/auth-status')) return json({ authenticated: true, username: 'owner' });
    if (path.endsWith('/csrf')) return json({ token: 'fixture-csrf' });
    if (path === '/api/owner/v1/proxy-pool/settings') {
      if (request.method() === 'PATCH') return state.settingsFailure ? json({ code: 'internal_error' }, 500) : json(pool);
      return json({ taskConcurrency: 3, targetHealthy: 3, probeConcurrency: 3 });
    }
    if (/\/proxy-pool\/nodes\/[^/]+$/.test(path) && request.method() === 'DELETE') {
      pool.nodes = pool.nodes.filter(node => node.id !== path.split('/').at(-1)); pool.total = pool.nodes.length; pool.healthyCount = pool.total;
      return json(pool);
    }
    if (path.endsWith('/proxy-pool/probe') && request.method() === 'POST') return json({ ...pool, verification: { removedCount: 0, retryCount: 1, diagnostics: [{ stage: 'platform', code: 'proxy_timeout', durationMs: 100, httpStatus: 0 }] } });
    if (path.endsWith('/standby-child-batches/selection') && request.method() === 'POST') return json({ scope: 'batch', count: 1, members: [account], previewToken: 'fixture-selection' });
    if (request.method() !== 'GET') { unexpectedWrites.push(path); return json({ code: 'unexpected_mutation' }, 400); }
    if (path === '/api/owner/v1/proxy-pool') return json(pool);
    if (path.endsWith('/target-accounts')) return state.accountReadFailure ? json({ code: 'internal_error' }, 503) : json({ items: [account], total: 1, page: 1, pageSize: 20 });
    if (path.endsWith('/operation-draft')) return json({ motherAccountId: mother, workspaceId: workspace, batchId: sourceId, executionBatchId: batchId, children: [{ accountId, identifier: account.identifier }], plannedAt: future });
    if (path === `/api/owner/v1/batches/${batchId}`) return json({ batch: operationBatch, targets: [account], targetTotal: operationBatch.targetCount });
    if (path.endsWith('/join-preview')) return json(preview);
    if (path.endsWith('/join-operation')) return partial ? json({ status: 'awaiting_login', targetTotal: 20, invitationConfirmedCount: 9, waitingSeatCount: 11, activeTaskCount: 0, blockedCount: 0, retryableInvitationCount: 0, succeededCount: 0, targets: [] }) : json({ code: 'not_found' }, 404);
    if (path.endsWith('/deliveries')) return json({ items: [], total: operationBatch.targetCount, readyCount: 0, cardCount: 0 });
    if (path === `/api/owner/v1/mother-accounts/${mother}`) return json({ id: mother, loginIdentifier: 'mother@example.test', status: 'active' });
    if (path.endsWith('/discovery')) return json({ status: 'discovered', workspaces: [{ id: workspace, displayName: '测试空间', accessStatus: 'readable' }] });
    if (path.endsWith('/access')) return json({ status: 'ready', expiresAt: future, exchangeId: 'fixture-exchange' });
    if (path.endsWith('/verification')) return json({ status: 'verified', permission: 'read', accessStatus: 'readable', completeness: 'complete', expiresAt: future, exchangeId: 'fixture-exchange', canManage: true, motherRole: 'owner', subscriptionStatus: 'active', activeUntil: future, seatEntitlements: { default: 1, prolite: 9 }, seatTypeCounts: { default: 1, prolite: 9 }, pendingInviteSeatTypeCounts: { default: 0, prolite: 102 }, members: [], readSources: ['subscriptions', 'seat_type_counts', 'workspace_members', 'outbound_invites'].map(source => ({ source, completeness: 'complete', permission: 'read', outcome: 'operational', observedAt: '2026-10-07T00:00:00Z' })) });
    if (path.endsWith('/mother-accounts')) return json({ items: [], total: 0, page: 1, pageSize: 20 });
    if (path.endsWith('/standby-child-batches')) return json({ items: [], total: 0, page: 1, pageSize: 20 });
    if (path === `/api/owner/v1/standby-child-batches/${sourceId}`) return json({ id: sourceId, name: '测试批次', version: 1, memberCount: 1 });
    return json({ code: 'not_found' }, 404);
  });
  return { context, page, state, errors, unexpectedWrites };
}

async function horizontalControl(page) {
  const control = page.locator('.task-concurrency-control').filter({ visible: true }).first();
  const label = await control.locator('label').boundingBox();
  const input = await control.getByRole('textbox', { name: '本次任务并发' }).boundingBox();
  assert.ok(label && input);
  assert.ok(label.x + label.width <= input.x, 'Concurrency label must be left of input');
  assert.ok(Math.abs(label.y + label.height / 2 - input.y - input.height / 2) < 3, 'Concurrency label and input must share a horizontal center');
}

test('action notices expire in three seconds, repeat safely and preserve actual page state', async t => {
  const browser = await launch();
  const f = await fixture(browser);
  const { page, state } = f;
  const settings = () => page.getByRole('button', { name: '运行参数', exact: true });
  const save = () => page.getByRole('dialog').getByRole('button', { name: '保存运行参数', exact: true });
  const openSettings = async () => { await page.clock.runFor(300); await settings().click(); await page.clock.runFor(250); };
  try {
    await page.goto(`${ownerURL}/owner/#proxy`);
    await t.test('remove result expires, record removal stays and internal counters are absent', async () => {
      await page.getByRole('button', { name: '移除', exact: true }).first().click(); await page.clock.runFor(250);
      await page.getByRole('dialog').getByRole('button', { name: '确认移除', exact: true }).click();
      const message = page.getByText('代理已移除', { exact: true }); await message.waitFor();
      await page.clock.runFor(2900); assert.equal(await message.count(), 1);
      await page.clock.runFor(200); assert.equal(await message.count(), 0);
      assert.equal(await page.locator('.proxy-list-panel tbody tr').count(), 2);
      assert.equal(await page.getByText(/最近补齐 · 需要/).count(), 0);
    });
    await t.test('repeated identical success gets a fresh three seconds', async () => {
      await openSettings(); await save().click();
      const message = page.getByText('运行参数已保存并生效', { exact: true }); await message.waitFor();
      await page.clock.runFor(2000);
      await openSettings(); await save().click(); await message.waitFor();
      await page.clock.runFor(1100); assert.equal(await message.count(), 1, 'Earlier timer cleared the newer result');
      await page.clock.runFor(2000); assert.equal(await message.count(), 0);
    });
    await t.test('failure appears inside its dialog and expires without discarding input', async () => {
      state.settingsFailure = true; await openSettings(); await save().click();
      const dialog = page.getByRole('dialog'); const message = dialog.getByText('操作未完成，请重试', { exact: true }); await message.waitFor();
      await page.clock.runFor(3100); assert.equal(await message.count(), 0);
      assert.equal(await dialog.getByRole('textbox', { name: '全局业务并发' }).inputValue(), '3');
      await save().click(); await message.waitFor();
      await dialog.getByRole('button', { name: '关闭提示', exact: true }).click(); assert.equal(await message.count(), 0);
      await page.keyboard.press('Escape'); await page.clock.runFor(250); state.settingsFailure = false;
    });
    await t.test('warning uses the same duration and does not report zero removals', async () => {
      await page.getByRole('button', { name: '验证全部', exact: true }).click();
      const message = page.getByText(/1 个代理暂时未通过/); await message.waitFor();
      assert.equal(await page.getByText(/已删除 0|已移除 0/).count(), 0);
      await page.clock.runFor(3100); assert.equal(await message.count(), 0);
    });
    await t.test('account toolbar stays horizontal at desktop and narrow widths', async () => {
      await page.goto(`${ownerURL}/owner/#accounts`);
      await page.getByRole('textbox', { name: '本次任务并发' }).waitFor();
      await horizontalControl(page); await page.setViewportSize({ width: 390, height: 844 }); await horizontalControl(page);
      await page.setViewportSize({ width: 1440, height: 1000 });
    });
    await t.test('expired read-error notice does not turn failure into an empty list', async () => {
      state.accountReadFailure = true; await page.reload();
      const message = page.getByRole('alert').filter({ hasText: '账号读取失败' }); await message.waitFor();
      await page.clock.runFor(3100); assert.equal(await message.count(), 0);
      await page.getByText('账号读取失败，请刷新重试', { exact: true }).waitFor();
      assert.equal(await page.getByText('暂无账号', { exact: true }).count(), 0);
    });
    assert.deepEqual(f.errors, []); assert.deepEqual(f.unexpectedWrites, []);
  } finally { await f.context.close(); await browser.close(); }
});

test('invitation step shows each blocker once and only offers OAuth for confirmed invitations', async t => {
  const browser = await launch();
  try {
    for (const partial of [false, true]) await t.test(partial ? 'partial confirmations can continue' : 'full seats with no confirmed invitation', async () => {
      const f = await fixture(browser, { partial });
      try {
        await f.page.goto(`${ownerURL}/owner/#operation`);
        await f.page.getByRole('button', { name: /第 2 步 发送邀请/ }).click();
        const message = f.page.getByText(`高级席位已满，${partial ? 11 : 1} 个账号等待席位`, { exact: true }); await message.waitFor();
        assert.equal(await message.count(), 1);
        assert.equal(await f.page.getByText(/本次可新增邀请|高级席位已开通|已确认邀请的账号可继续/).count(), 0);
        assert.equal(await f.page.getByRole('columnheader', { name: '执行阶段' }).count(), 0);
        assert.equal(await f.page.getByRole('button', { name: '继续 OAuth 登录', exact: true }).count(), partial ? 1 : 0);
        await horizontalControl(f.page);
        await f.page.clock.runFor(3100); assert.equal(await message.count(), 1, 'A real blocking condition must remain visible');
        if (partial) {
          await f.page.getByRole('button', { name: '继续 OAuth 登录', exact: true }).click();
          await f.page.getByRole('button', { name: '执行已确认账号 OAuth 登录', exact: true }).waitFor();
        }
        assert.deepEqual(f.errors, []); assert.deepEqual(f.unexpectedWrites, []);
      } finally { await f.context.close(); }
    });
  } finally { await browser.close(); }
});

test('Public action failure expires while the redeemed record remains', async () => {
  const browser = await launch(); const context = await browser.newContext(); const page = await context.newPage();
  await page.clock.install();
  await page.route('**/api/public/**', route => route.fulfill(route.request().url().endsWith('/state') ? { json: { hasOrder: true, cardSuffix: 'fixture8', canAccess: true, deliveryStatus: 'available', deliveryFormat: 'zip', accountCount: 1 } } : { status: 503, json: { code: 'public_unavailable' } }));
  try {
    await page.goto(`${publicURL}/redeem/`);
    await page.getByRole('button', { name: '下载 ZIP', exact: true }).click();
    const message = page.getByRole('alert'); await message.waitFor();
    await page.clock.runFor(3100); assert.equal(await message.count(), 0);
    assert.equal(await page.getByRole('button', { name: '下载 ZIP', exact: true }).count(), 1);
  } finally { await context.close(); await browser.close(); }
});
