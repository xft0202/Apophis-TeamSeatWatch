// Run against the local Owner frontend; all API calls use isolated fixtures.
import { chromium } from '/root/.local/share/playwright-cli/node_modules/playwright/index.mjs';
import assert from 'node:assert/strict';
import { mkdir } from 'node:fs/promises';
import { test } from 'node:test';

const ownerURL = process.env.TSW_OWNER_BROWSER_URL ?? 'http://127.0.0.1:5177';
const accounts = Array.from({ length: 21 }, (_, index) => ({
  id: `00000000-0000-4000-8000-${String(index + 1).padStart(12, '0')}`,
  identifier: `account${String(index + 1).padStart(2, '0')}@example.test`,
  displayLabel: '', status: 'active', hasPassword: true, hasTotp: true, hasRecovery: false,
  materialStatus: 'complete', secretRevision: 1, version: 1, updatedAt: '2026-10-06T00:00:00Z',
  latestProbeStatus: index === 0 ? 'unprobed' : 'available',
}));

test('new batch lists accounts, uses table selection and explains required names', async (t) => {
  const browser = await chromium.launch({
    executablePath: process.env.TSW_CHROMIUM_PATH ?? '/root/.cache/ms-playwright/chromium-1243/chrome-linux64/chrome',
    headless: true, args: ['--no-sandbox'],
  });
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, colorScheme: 'light' });
  const page = await context.newPage();
  page.setDefaultTimeout(3000);
  page.setDefaultNavigationTimeout(15000);
  const errors = [];
  const lists = [];
  const previews = [];
  const writes = [];
  const batches = [];
  let transferAccountId = null;
  const sourceBatch = { id: '20000000-0000-4000-8000-000000000001', name: '原批次' };
  page.on('pageerror', (error) => errors.push(error.message));
  await page.route('**/api/owner/**', (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const json = (body) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
    if (url.pathname.endsWith('/auth-status')) return json({ authenticated: true, username: 'owner' });
    if (url.pathname.endsWith('/csrf')) return json({ token: 'fixture-csrf' });
    if (url.pathname.endsWith('/target-accounts')) {
      const query = Object.fromEntries(url.searchParams);
      lists.push(query);
      const filtered = accounts.filter((account) => (!query.domain || account.identifier.endsWith(`@${query.domain.replace(/^@/, '')}`)) && (!query.probe_status || account.latestProbeStatus === query.probe_status) && (!query.membership_status || (query.membership_status === 'assigned') === !!account.standbyBatch));
      const pageNumber = Number(query.page);
      const pageSize = Number(query.page_size);
      return json({ items: filtered.slice((pageNumber - 1) * pageSize, pageNumber * pageSize), total: filtered.length, page: pageNumber, pageSize });
    }
    if (url.pathname.endsWith('/standby-child-batches/selection')) {
      const selection = request.postDataJSON();
      previews.push(selection);
      const members = (selection.accountIds ?? []).map((accountId) => ({ accountId, membershipVersion: accountId === transferAccountId ? 1 : 0, ...(accountId === transferAccountId ? { currentBatch: sourceBatch } : {}) }));
      return json({ scope: selection.scope, count: members.length, members });
    }
    if (url.pathname.endsWith('/standby-child-batches')) {
      if (request.method() === 'GET') return json({ items: batches, total: batches.length, page: 1, pageSize: 20 });
      const write = request.postDataJSON();
      writes.push(write);
      const batch = { id: `10000000-0000-4000-8000-${String(batches.length + 1).padStart(12, '0')}`, name: write.name, version: 1, memberCount: write.expectedCount, domainCount: 1, domains: ['example.test'], updatedAt: '2026-10-06T00:00:00Z' };
      batches.push(batch);
      return json(batch);
    }
    return route.fulfill({ status: 404, contentType: 'application/json', body: '{"code":"not_found"}' });
  });
  const button = (name) => page.getByRole('button', { name, exact: true });
  const open = async () => {
    await page.goto(`${ownerURL}/owner/#batches`, { waitUntil: 'domcontentloaded' });
    await page.reload({ waitUntil: 'domcontentloaded' });
    await button('新建批次').click();
    await page.getByRole('heading', { name: '新建批次', exact: true }).waitFor();
  };
  const filter = async () => {
    await page.getByRole('textbox', { name: '邮箱域名', exact: true }).fill('example.test');
    await page.getByRole('checkbox', { name: `选择 ${accounts[1].identifier}`, exact: true }).waitFor();
  };
  try {
    await t.test('opening immediately requests the first page including unprobed accounts', async () => {
      await open();
      await page.getByRole('checkbox', { name: `选择 ${accounts[0].identifier}`, exact: true }).waitFor();
      assert.equal(await page.locator('.batch-account-table tbody tr').count(), 20);
      assert.equal(lists.at(-1).page, '1');
      assert.equal(lists.at(-1).page_size, '20');
      assert.equal(lists.at(-1).domain, undefined);
      assert.equal(lists.at(-1).probe_status, undefined);
      assert.equal(lists.at(-1).membership_status, undefined);
    });
    await t.test('selected count has no duplicate select-all or clear buttons', async () => {
      await open();
      await filter();
      await page.getByRole('checkbox', { name: `选择 ${accounts[1].identifier}`, exact: true }).check();
      await page.getByText('已选 1 个账号', { exact: true }).waitFor();
      assert.equal(await page.getByRole('button', { name: /选择全部筛选结果|取消选择/ }).count(), 0);
    });
    await t.test('the header toggles only the displayed page and preserves cross-page selection', async () => {
      await open();
      const header = page.getByRole('checkbox', { name: '选择本页', exact: true });
      await header.check();
      await page.getByText('已选 20 个账号', { exact: true }).waitFor();
      await button('2').click();
      await page.getByRole('checkbox', { name: `选择 ${accounts[20].identifier}`, exact: true }).waitFor();
      assert.equal(await header.isChecked(), false);
      await header.check();
      await page.getByText('已选 21 个账号', { exact: true }).waitFor();
      await header.uncheck();
      await page.getByText('已选 20 个账号', { exact: true }).waitFor();
      await button('1').click();
      await page.getByRole('checkbox', { name: `选择 ${accounts[0].identifier}`, exact: true }).waitFor();
      assert.equal(await header.isChecked(), true);
      await header.uncheck();
      assert.equal(await page.locator('.account-selection-bar').count(), 0);
      assert.equal(await button('保存批次').isDisabled(), true);
      await header.check();
      await button('2').click();
      await page.getByRole('checkbox', { name: `选择 ${accounts[20].identifier}`, exact: true }).check();
      await page.getByRole('textbox', { name: '批次名称', exact: true }).fill('跨页批次');
      await button('保存批次').click();
      await page.getByText('已保存批次 跨页批次 · 21 个账号', { exact: true }).waitFor();
      assert.deepEqual(previews.at(-1).accountIds, accounts.map((account) => account.id));
      assert.equal(writes.at(-1).expectedCount, 21);
    });
    await t.test('changing domain clears selection and reset loads all records again', async () => {
      await open();
      await page.getByRole('checkbox', { name: `选择 ${accounts[1].identifier}`, exact: true }).check();
      await page.getByRole('textbox', { name: '邮箱域名', exact: true }).fill('missing.test');
      await page.getByText('无匹配账号', { exact: true }).waitFor();
      assert.equal(await page.locator('.account-selection-bar').count(), 0);
      assert.equal(await button('保存批次').isDisabled(), true);
      await button('重置').click();
      await page.getByRole('checkbox', { name: `选择 ${accounts[0].identifier}`, exact: true }).waitFor();
      assert.equal(await page.locator('.batch-account-table tbody tr').count(), 20);
      assert.equal(lists.at(-1).domain, undefined);
      const size = page.getByRole('combobox', { name: '每页条数', exact: true });
      await size.click();
      await page.getByRole('option', { name: '50 条 / 页', exact: true }).click();
      await page.getByRole('checkbox', { name: `选择 ${accounts[20].identifier}`, exact: true }).waitFor();
      assert.equal(await page.locator('.batch-account-table tbody tr').count(), 21);
      assert.equal(lists.at(-1).page_size, '50');
    });
    await t.test('selected accounts can submit and a missing name shows inline validation', async () => {
      await open();
      await filter();
      for (const account of accounts.slice(1, 4)) await page.getByRole('checkbox', { name: `选择 ${account.identifier}`, exact: true }).check();
      await page.getByText('已选 3 个账号', { exact: true }).waitFor();
      const writesBefore = writes.length;
      const previewsBefore = previews.length;
      assert.equal(await button('保存批次').isDisabled(), false, 'Selected accounts must allow Save to explain a missing name');
      await button('保存批次').click();
      await page.getByText('请输入批次名称', { exact: true }).waitFor();
      assert.equal(writes.length, writesBefore);
      assert.equal(previews.length, previewsBefore);
      const name = page.getByRole('textbox', { name: '批次名称', exact: true });
      assert.equal(await name.evaluate((element) => element === document.activeElement), true);
      await name.fill('测试批次');
      await button('保存批次').click();
      await page.getByText('已保存批次 测试批次 · 3 个账号', { exact: true }).waitFor();
      assert.deepEqual(previews.at(-1), { scope: 'selected', accountIds: accounts.slice(1, 4).map((account) => account.id) });
      assert.equal(writes.at(-1).name, '测试批次');
      assert.equal(writes.at(-1).expectedCount, 3);
    });
    await t.test('assigned accounts remain visible and moving them still requires confirmation', async () => {
      transferAccountId = accounts[0].id;
      accounts[0].standbyBatch = sourceBatch;
      try {
        await open();
        await page.getByRole('checkbox', { name: `选择 ${accounts[0].identifier}`, exact: true }).check();
        await page.getByText('原批次', { exact: true }).waitFor();
        await page.getByRole('textbox', { name: '批次名称', exact: true }).fill('移批测试');
        const before = writes.length;
        await button('保存批次').click();
        const confirmation = page.getByRole('dialog', { name: '确认移批', exact: true });
        await confirmation.waitFor();
        assert.equal(writes.length, before);
        await confirmation.getByRole('button', { name: '取消', exact: true }).click();
        assert.equal(writes.length, before);
        await page.getByText('已选 1 个账号', { exact: true }).waitFor();
        await button('保存批次').click();
        await confirmation.getByRole('button', { name: '确认移批并保存', exact: true }).click();
        await page.getByText('已保存批次 移批测试 · 1 个账号', { exact: true }).waitFor();
        assert.equal(writes.length, before + 1);
        assert.deepEqual(writes.at(-1).selection.members, [{ accountId: accounts[0].id, membershipVersion: 1 }]);
      } finally {
        transferAccountId = null;
        delete accounts[0].standbyBatch;
      }
    });
    await t.test('selection and validation fit desktop, dark mode and narrow screens', async () => {
      await open();
      await page.getByRole('checkbox', { name: `选择 ${accounts[1].identifier}`, exact: true }).check();
      await button('保存批次').click();
      await page.getByText('请输入批次名称', { exact: true }).waitFor();
      const evidence = process.env.TSW_BROWSER_EVIDENCE;
      for (const appearance of ['light', 'dark', 'narrow']) {
        if (appearance === 'dark') await button('深色模式').click();
        if (appearance === 'narrow') {
          await button('浅色模式').click();
          await page.setViewportSize({ width: 390, height: 844 });
        }
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, `${appearance}: page must fit the viewport`);
        if (evidence) {
          await mkdir(evidence, { recursive: true });
          await page.screenshot({ path: `${evidence}/new-batch-${appearance}.png`, fullPage: true });
        }
      }
      await page.setViewportSize({ width: 1440, height: 1000 });
    });
    await t.test('account management retains its explicit selection actions', async () => {
      await page.goto(`${ownerURL}/owner/#accounts`, { waitUntil: 'domcontentloaded' });
      await page.getByRole('checkbox', { name: `选择 ${accounts[1].identifier}`, exact: true }).check();
      assert.equal(await page.getByRole('button', { name: /选择全部筛选结果/ }).count(), 1);
      await button('取消选择').click();
      assert.equal(await page.locator('.account-selection-bar:visible').count(), 0);
    });
    assert.deepEqual(errors, []);
  } finally {
    await context.close();
    await browser.close();
  }
});
