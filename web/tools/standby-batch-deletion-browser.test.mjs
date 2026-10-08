// Run against a built Owner frontend; all API calls use isolated records.
import { chromium } from '/root/.local/share/playwright-cli/node_modules/playwright/index.mjs';
import assert from 'node:assert/strict';
import { mkdir } from 'node:fs/promises';
import { test } from 'node:test';

const ownerURL = process.env.TSW_OWNER_BROWSER_URL ?? 'http://127.0.0.1:5189';
const makeBatch = (index, memberCount = 3) => ({
  id: `10000000-0000-4000-8000-${String(index).padStart(12, '0')}`,
  name: `待用批次 ${String(index).padStart(2, '0')}`, version: 4, memberCount,
  domainCount: memberCount ? 1 : 0, domains: memberCount ? ['example.test'] : [], updatedAt: '2026-10-06T17:02:00Z',
});

test('batch deletion confirms scope, retains accounts and keeps list state', async (t) => {
  const browser = await chromium.launch({ executablePath: process.env.TSW_CHROMIUM_PATH ?? '/root/.cache/ms-playwright/chromium-1243/chrome-linux64/chrome', headless: true, args: ['--no-sandbox'] });
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, timezoneId: 'Asia/Shanghai' });
  const page = await context.newPage();
  page.setDefaultTimeout(4000);
  const errors = [];
  page.on('pageerror', (error) => errors.push(error.message));
  let batches = [];
  let accounts = [];
  let writes = [];
  let lists = [];
  let failure = null;
  let release = null;
  let slow = false;
  await page.route('**/api/owner/**', async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const json = (body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
    if (url.pathname.endsWith('/auth-status')) return json({ authenticated: true, username: 'owner' });
    if (url.pathname.endsWith('/csrf')) return json({ token: 'fixture-csrf' });
    if (url.pathname.endsWith('/target-accounts')) {
      const query = Object.fromEntries(url.searchParams);
      return json({ items: accounts, total: accounts.length, page: Number(query.page), pageSize: Number(query.page_size) });
    }
    if (url.pathname.endsWith('/standby-child-batches')) {
      assert.equal(request.method(), 'GET');
      const query = Object.fromEntries(url.searchParams);
      lists.push(query);
      const filtered = batches.filter((batch) => (!query.search || batch.name.includes(query.search)) && (!query.domain || batch.domains.includes(query.domain)));
      const pageNumber = Number(query.page);
      const pageSize = Number(query.page_size);
      return json({ items: filtered.slice((pageNumber - 1) * pageSize, pageNumber * pageSize), total: filtered.length, page: pageNumber, pageSize });
    }
    if (request.method() === 'DELETE' && url.pathname.includes('/standby-child-batches/')) {
      const id = url.pathname.split('/').at(-1);
      writes.push({ id, body: request.postDataJSON(), csrf: request.headers()['x-csrf-token'] });
      if (slow) await new Promise((resolve) => { release = resolve; });
      if (failure) return json({ status: failure, code: failure === 409 ? 'selection_changed' : 'internal_error' }, failure);
      batches = batches.filter((batch) => batch.id !== id);
      accounts = accounts.map((account) => { const result = { ...account }; if (result.standbyBatch?.id === id) delete result.standbyBatch; return result; });
      return route.fulfill({ status: 204 });
    }
    return json({ code: 'not_found' }, 404);
  });
  const button = (name) => page.getByRole('button', { name, exact: true });
  const row = (batch) => page.locator('.batch-list-table tbody tr').filter({ has: page.getByText(batch.name, { exact: true }) });
  const dialog = () => page.getByRole('dialog', { name: '删除批次', exact: true });
  async function open(records = [makeBatch(1)]) {
    batches = records;
    accounts = Array.from({ length: 3 }, (_, index) => ({ id: `20000000-0000-4000-8000-${String(index + 1).padStart(12, '0')}`, identifier: `account${index + 1}@example.test`, displayLabel: '', status: 'active', hasPassword: true, hasTotp: true, hasRecovery: false, materialStatus: 'complete', secretRevision: 1, version: 1, updatedAt: '2026-10-06T17:02:00Z', standbyBatch: { id: records[0].id, name: records[0].name } }));
    writes = []; lists = []; failure = null; slow = false; release = null;
    await page.goto(`${ownerURL}/owner/#batches`, { waitUntil: 'domcontentloaded' });
    await page.reload({ waitUntil: 'domcontentloaded' });
    await row(records[0]).getByRole('button', { name: '删除', exact: true }).waitFor();
  }
  try {
    await t.test('cancel shows exact scope and sends no mutation', async () => {
      const batch = makeBatch(1);
      await open([batch]);
      await row(batch).getByRole('button', { name: '删除', exact: true }).click();
      await dialog().getByText(`确认删除「${batch.name}」？3 个账号将解除该批次归属。`, { exact: true }).waitFor();
      await dialog().getByText('账号资料、凭据及已执行的交付和清退记录保留。', { exact: true }).waitFor();
      assert.equal(writes.length, 0);
      await dialog().getByRole('button', { name: '取消', exact: true }).click();
      await dialog().waitFor({ state: 'hidden' });
      assert.equal(writes.length, 0);
      assert.equal(await row(batch).count(), 1);
    });
    await t.test('confirm sends one versioned CSRF request and keeps the accounts', async () => {
      const batch = makeBatch(1);
      await open([batch]);
      slow = true;
      await row(batch).getByRole('button', { name: '删除', exact: true }).click();
      await dialog().getByRole('button', { name: '确认删除', exact: true }).click();
      await row(batch).getByText('正在删除批次', { exact: true }).waitFor();
      assert.equal(writes.length, 1);
      assert.equal(await dialog().getByRole('button', { name: '取消', exact: true }).isDisabled(), true);
      assert.equal(await dialog().getByRole('button', { name: '确认删除', exact: true }).isDisabled(), true);
      await page.keyboard.press('Escape');
      assert.equal(await dialog().isVisible(), true);
      release();
      await page.getByText(`已删除批次 ${batch.name} · 3 个账号已解除归属`, { exact: true }).waitFor();
      await page.getByText('暂无批次', { exact: true }).waitFor();
      assert.deepEqual(writes, [{ id: batch.id, body: { expectedVersion: 4, expectedCount: 3, confirmed: true }, csrf: 'fixture-csrf' }]);
      await button('新建批次').click();
      await page.getByRole('checkbox', { name: '选择 account1@example.test', exact: true }).waitFor();
      assert.equal(await page.locator('.batch-account-table tbody tr').count(), 3);
      assert.equal(await page.locator('.batch-account-table tbody').getByText('未分批', { exact: true }).count(), 3);
    });
    await t.test('empty batches can be deleted', async () => {
      const batch = makeBatch(1, 0);
      await open([batch]);
      await row(batch).getByRole('button', { name: '删除', exact: true }).click();
      await dialog().getByText(`确认删除「${batch.name}」？0 个账号将解除该批次归属。`, { exact: true }).waitFor();
      await dialog().getByRole('button', { name: '确认删除', exact: true }).click();
      await page.getByText('暂无批次', { exact: true }).waitFor();
      assert.equal(writes[0].body.expectedCount, 0);
    });
    await t.test('changed batches refuse deletion and require refreshed confirmation', async () => {
      const batch = makeBatch(1);
      await open([batch]);
      failure = 409;
      await row(batch).getByRole('button', { name: '删除', exact: true }).click();
      await dialog().getByRole('button', { name: '确认删除', exact: true }).click();
      await dialog().waitFor({ state: 'hidden' });
      await row(batch).getByText('批次或账号已变化，请刷新列表后重试', { exact: true }).waitFor();
      assert.equal(await row(batch).count(), 1);
      batches[0] = { ...batch, version: 5, memberCount: 4 };
      failure = null;
      await button('刷新').click();
      await row(batch).getByText('4', { exact: true }).waitFor();
      await row(batch).getByRole('button', { name: '删除', exact: true }).click();
      await dialog().getByText(`确认删除「${batch.name}」？4 个账号将解除该批次归属。`, { exact: true }).waitFor();
      await dialog().getByRole('button', { name: '确认删除', exact: true }).click();
      await page.getByText('暂无批次', { exact: true }).waitFor();
      assert.equal(writes[1].body.expectedVersion, 5);
      assert.equal(writes[1].body.expectedCount, 4);
    });
    await t.test('failed requests retain the row and release controls for retry', async () => {
      const batch = makeBatch(1);
      await open([batch]); failure = 500;
      await row(batch).getByRole('button', { name: '删除', exact: true }).click();
      await dialog().getByRole('button', { name: '确认删除', exact: true }).click();
      await dialog().waitFor({ state: 'hidden' });
      await row(batch).getByText('操作未完成，请重试', { exact: true }).waitFor();
      assert.equal(await row(batch).getByRole('button', { name: '删除', exact: true }).isEnabled(), true);
    });
    await t.test('deleting the last record on page two preserves filters and returns to page one', async () => {
      const records = Array.from({ length: 21 }, (_, index) => makeBatch(index + 1));
      await open(records);
      await page.getByRole('textbox', { name: '搜索批次', exact: true }).fill('待用批次');
      await page.getByRole('textbox', { name: '邮箱域名', exact: true }).fill('example.test');
      await button('2').click();
      const last = records[20];
      await row(last).getByRole('button', { name: '删除', exact: true }).click();
      await dialog().getByRole('button', { name: '确认删除', exact: true }).click();
      await row(records[0]).waitFor();
      assert.equal(await page.locator('.batch-list-table tbody tr').count(), 20);
      assert.equal(lists.at(-1).page, '1');
      assert.equal(lists.at(-1).page_size, '20');
      assert.equal(lists.at(-1).search, '待用批次');
      assert.equal(lists.at(-1).domain, 'example.test');
      assert.equal(await page.getByRole('textbox', { name: '搜索批次', exact: true }).inputValue(), '待用批次');
    });
    await t.test('dark and narrow layouts keep time on one line and show the confirmation', async () => {
      const batch = makeBatch(1);
      await open([batch]);
      const evidence = process.env.TSW_BROWSER_EVIDENCE;
      if (evidence) await mkdir(evidence, { recursive: true });
      for (const [scheme, width] of [['light', 1440], ['dark', 1440], ['light', 390]]) {
        await page.emulateMedia({ colorScheme: scheme });
        await page.setViewportSize({ width, height: 900 });
        const color = await row(batch).getByRole('button', { name: '删除', exact: true }).evaluate((element) => ({ color: getComputedStyle(element).color, variable: getComputedStyle(element).getPropertyValue('--button-color'), style: element.getAttribute('style'), disabled: element.hasAttribute('data-disabled') }));
        const [red, green, blue] = color.color.match(/\d+/g).map(Number);
        assert.ok(red > green + 30 && red > blue + 30, `Delete must retain the destructive theme color: ${JSON.stringify(color)}`);
        const stamp = row(batch).locator('.date-time');
        assert.equal(await stamp.evaluate((element) => getComputedStyle(element).whiteSpace), 'nowrap');
        await row(batch).getByRole('button', { name: '删除', exact: true }).click();
        await dialog().waitFor();
        assert.equal(await dialog().getByRole('button', { name: '确认删除', exact: true }).isVisible(), true);
        if (evidence) await page.screenshot({ path: `${evidence}/delete-${scheme}-${width}.png`, fullPage: true });
        await dialog().getByRole('button', { name: '取消', exact: true }).click();
      }
    });
    assert.deepEqual(errors, []);
  } finally {
    release?.();
    await browser.close();
  }
});
