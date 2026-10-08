// Run against the local Owner frontend; all API calls use isolated fixtures.
import { chromium } from '/root/.local/share/playwright-cli/node_modules/playwright/index.mjs';
import assert from 'node:assert/strict';
import { mkdir } from 'node:fs/promises';
import { test } from 'node:test';

const ownerURL = process.env.TSW_OWNER_BROWSER_URL ?? 'http://127.0.0.1:5177';
const count = Number(process.env.TSW_IMPORT_BROWSER_COUNT ?? 20);
const source = Array.from({ length: count }, (_, index) => `import${index + 1}@example.test----fixture-password----JBSWY3DPEHPK3PXP`).join('\n');

test('confirmed account import shows results without offering preview again', async (t) => {
  const browser = await chromium.launch({
    executablePath: process.env.TSW_CHROMIUM_PATH ?? '/root/.cache/ms-playwright/chromium-1243/chrome-linux64/chrome',
    headless: true, args: ['--no-sandbox'],
  });
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, colorScheme: 'light' });
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  const errors = [];
  const imports = [];
  const lists = [];
  let importResult = null;
  let importFailure = 0;
  page.on('pageerror', (error) => errors.push(error.message));
  await page.route('**/api/owner/**', (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    const json = (body) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
    if (path.endsWith('/auth-status')) return json({ authenticated: true, username: 'owner', passwordChangedAt: '2026-10-06T00:00:00Z' });
    if (path.endsWith('/csrf')) return json({ token: 'fixture-csrf' });
    if (path.endsWith('/target-accounts')) {
      const url = new URL(request.url());
      lists.push({ page: url.searchParams.get('page'), pageSize: url.searchParams.get('page_size') });
      return json({ items: [], total: 0, page: 1, pageSize: 20 });
    }
    if (path.endsWith('/child-materials/import')) {
      const content = request.postDataJSON().content;
      imports.push(content);
      if (importFailure) return route.fulfill({ status: importFailure, contentType: 'application/json', body: '{"code":"forbidden"}' });
      const lines = content.split('\n');
      return json(importResult ?? { imported: lines.length, duplicate: 0, invalid: 0, rows: lines.map((line, index) => ({ line: index + 1, status: 'imported', identifier: line.split('----')[0] })) });
    }
    return route.fulfill({ status: 404, contentType: 'application/json', body: '{"code":"not_found"}' });
  });
  const button = (name) => page.getByRole('button', { name, exact: true });
  const input = () => page.getByRole('textbox', { name: '账号资料文本', exact: true });
  const step = async (expected) => assert.equal((await page.locator('.account-import-step.is-current').innerText()).replace(/\s+/g, ' ').trim(), expected);
  const preview = async (content) => {
    const requests = imports.length;
    await input().fill(content);
    await button('预览导入').click();
    await page.getByText(`导入预览 · ${content.split('\n').length} 行`, { exact: true }).waitFor();
    await step('02 核对账号');
    assert.equal(imports.length, requests, 'Preview must not save accounts');
  };
  const result = async (summary) => {
    await button('确认导入').click();
    await page.getByText(summary, { exact: true }).waitFor();
    assert.equal(await button('预览导入').count(), 0, 'The result step must not offer preview import again');
    assert.equal(await input().count(), 0, 'The result step must not render the source editor');
    await step('03 导入结果');
  };
  try {
    await page.goto(`${ownerURL}/owner/#accounts`, { waitUntil: 'domcontentloaded' });
    await button('导入账号').click();
    await t.test('confirmed import replaces the source editor with the actual result', async () => {
      await step('01 录入资料');
      await preview(source);
      await result(`已保存 ${count} · 重复 0 · 无效 0`);
      assert.deepEqual(imports, [source]);
    });
    await t.test('result layout fits desktop and narrow screens in light and dark mode', async () => {
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
          await page.screenshot({ path: `${evidence}/import-result-${appearance}.png`, fullPage: true });
        }
      }
      await page.setViewportSize({ width: 1440, height: 1000 });
    });
    await t.test('continue import clears the previous source and result', async () => {
      await button('继续导入').click();
      await step('01 录入资料');
      assert.equal(await input().inputValue(), '');
      assert.equal(await button('预览导入').isDisabled(), true);
      assert.equal(await page.getByText(`已保存 ${count} · 重复 0 · 无效 0`, { exact: true }).count(), 0);
      assert.deepEqual(imports, [source]);
      await page.locator('input[type=file]').setInputFiles({ name: 'accounts.txt', mimeType: 'text/plain', buffer: Buffer.from(source) });
      await button('预览导入').click();
      await result(`已保存 ${count} · 重复 0 · 无效 0`);
    });
    await t.test('returning to the list and reopening import starts a fresh first step', async () => {
      await button('返回账号列表').click();
      await page.getByRole('heading', { name: '账号管理', exact: true }).waitFor();
      await page.getByText('暂无账号', { exact: true }).waitFor();
      assert.ok(lists.length >= 2, 'Returning refreshes the current account page');
      assert.ok(lists.every((request) => request.page === '1' && request.pageSize === '20'));
      await button('导入账号').click();
      await step('01 录入资料');
      assert.equal(await input().inputValue(), '');
      assert.equal(await page.locator('input[type=file]').inputValue(), '');
      assert.equal(await page.getByText(`已保存 ${count} · 重复 0 · 无效 0`, { exact: true }).count(), 0);
    });
    await t.test('mixed results preserve row numbers and repair only failed rows', async () => {
      const lines = Array.from({ length: 3 }, (_, index) => `mixed${index + 1}@example.test----fixture-password----JBSWY3DPEHPK3PXP`);
      importResult = { imported: 1, duplicate: 1, invalid: 1, rows: ['imported', 'duplicate', 'invalid'].map((status, index) => ({ line: index + 1, status, identifier: `mixed${index + 1}@example.test` })) };
      await preview(lines.join('\n'));
      await result('已保存 1 · 重复 1 · 无效 1');
      await page.getByText('第 2 行 · 重复 · mixed2@example.test', { exact: true }).waitFor();
      await page.getByText('第 3 行 · 无效 · mixed3@example.test', { exact: true }).waitFor();
      await button('修正未导入').click();
      await step('01 录入资料');
      assert.equal(await input().inputValue(), lines[2]);
      assert.equal(await page.getByText('已保存 1 · 重复 1 · 无效 1', { exact: true }).count(), 0);
      importResult = null;
      await preview(lines[2]);
      await result('已保存 1 · 重复 0 · 无效 0');
      assert.deepEqual(imports.slice(-2), [lines.join('\n'), lines[2]]);
    });
    await t.test('a rejected save keeps the preview and retries the preserved input', async () => {
      await button('继续导入').click();
      await preview(source);
      importFailure = 403;
      await button('确认导入').click();
      await page.getByText('没有保存权限', { exact: true }).waitFor();
      await step('02 核对账号');
      assert.equal(await button('继续导入').count(), 0);
      await button('返回修改').click();
      assert.equal(await input().inputValue(), source);
      await preview(source);
      importFailure = 0;
      await result(`已保存 ${count} · 重复 0 · 无效 0`);
      assert.deepEqual(imports.slice(-2), [source, source]);
    });
    await t.test('historical accounts needing 2FA retain their repair action', async () => {
      await button('继续导入').click();
      importResult = { imported: 1, duplicate: 0, invalid: 0, rows: [{ line: 1, status: 'needs_totp', identifier: 'import1@example.test' }] };
      await preview(source.split('\n')[0]);
      await result('已保存 1 · 重复 0 · 无效 0');
      assert.equal(await button('补齐2FA').count(), 1);
    });
    assert.deepEqual(errors, []);
  } finally {
    await context.close();
    await browser.close();
  }
});
