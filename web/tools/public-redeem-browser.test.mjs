import test from 'node:test';
import assert from 'node:assert/strict';
import { chromium } from '/root/.local/share/playwright-cli/node_modules/playwright/index.mjs';
const origin = process.env.TSW_PUBLIC_BROWSER_URL ?? 'http://127.0.0.1:5193';
const zip = Buffer.from('UEsDBBQAAAAAADh+R11CjxzaEAAAABAAAAAjAAAAYXBvcGhpcy10ZWFtc2VhdHdhdGNoLWRlbGl2ZXJ5Lmpzb257ImZpeHR1cmUiOnRydWV9UEsBAhQDFAAAAAAAOH5HXUKPHNoQAAAAEAAAACMAAAAAAAAAAAAAAIABAAAAAGFwb3BoaXMtdGVhbXNlYXR3YXRjaC1kZWxpdmVyeS5qc29uUEsFBgAAAAABAAEAUQAAAFEAAAAAAA==', 'base64');
const filename = 'Apophis-TeamSeatWatch-2026-10-07-00-00-00.zip';
async function fixture(browser, { downloadFails = false, existingOrder = true } = {}) {
  const context = await browser.newContext({ acceptDownloads: true });
  let selected;
  const calls = [], errors = [], downloads = [];
  await context.route('**/api/public/**', route => {
    const path = new URL(route.request().url()).pathname;
    const card = route.request().postDataJSON()?.cardSecret;
    calls.push({ path, card, selected });
    const json = (body, status = 200) => route.fulfill({ status, json: body });
    if (path.endsWith('/confirm')) {
      selected = card;
      return json({ action: 'restored', cardSuffix: card.slice(-8), hasOrder: true, canClaim: false, canAccess: true, remainingSeconds: 3600, deliveryStatus: 'available', deliveryFormat: 'zip', accountCount: 1, livenessStatus: 'healthy' });
    }
    if (path.endsWith('/credential-status')) return json({ status: 'healthy', checkQueued: false, hasOrder: existingOrder });
    if (path.endsWith('/download')) return downloadFails ? json({ code: 'public_unavailable' }, 503) : route.fulfill({ contentType: 'application/zip', headers: { 'Content-Disposition': `attachment; filename="${filename}"` }, body: zip });
    if (path.endsWith('/state')) return json({ code: 'public_request_denied' }, 404);
    return json({ code: 'unexpected_request' }, 400);
  });
  const page = await context.newPage();
  page.on('pageerror', e => errors.push(e.message));
  page.on('download', event => downloads.push(event));
  await page.goto(origin + '/redeem/');
  await page.getByRole('button', { name: '兑换卡密', exact: true }).waitFor();
  return { context, page, calls, errors, downloads };
}
const launch = () => chromium.launch({ executablePath: '/root/.cache/ms-playwright/chromium-1243/chrome-linux64/chrome', headless: true, args: ['--no-sandbox'] });
test('redemption automatically downloads each ZIP before changing the selected card, and healthy checks retain download', async () => {
  const browser = await launch(); const f = await fixture(browser);
  try {
    await f.page.getByRole('textbox', { name: '卡密', exact: true }).fill('fixture-card-a\nfixture-card-b');
    await f.page.getByRole('button', { name: '兑换卡密', exact: true }).click();
    await f.page.waitForFunction(() => document.querySelectorAll('.public-result-row').length === 2 && !document.querySelector('.public-input-panel button')?.hasAttribute('data-loading'));
    assert.equal(f.downloads.length, 2, 'Successful redemption must initiate actual browser ZIP downloads');
    assert.deepEqual(f.calls.filter(call => /confirm|download/.test(call.path)).map(call => [call.path.split('/').at(-1), call.card ?? call.selected]), [['confirm', 'fixture-card-a'], ['download', 'fixture-card-a'], ['confirm', 'fixture-card-b'], ['download', 'fixture-card-b']]);
    assert.ok(f.downloads.every(download => download.suggestedFilename() === filename));
    const row = f.page.locator('.public-result-row').first();
    await row.getByRole('button', { name: '401 找回', exact: true }).click();
    await row.getByText('凭据正常', { exact: true }).waitFor();
    assert.equal(await row.getByRole('button', { name: '下载 ZIP', exact: true }).isVisible(), true);
    assert.equal(await row.getByText('待处理', { exact: true }).count(), 0);
    assert.equal(f.calls.filter(call => call.path.endsWith('/reclaim')).length, 0);
    assert.deepEqual(f.errors, []);
  } finally { await f.context.close(); await browser.close(); }
});
test('a failed stream keeps the redeemed result and offers a ZIP retry', async () => {
  const browser = await launch(); const f = await fixture(browser, { downloadFails: true });
  try {
    await f.page.getByRole('textbox', { name: '卡密', exact: true }).fill('fixture-card-a');
    await f.page.getByRole('button', { name: '兑换卡密', exact: true }).click();
    await f.page.getByText('下载未完成', { exact: true }).waitFor();
    assert.equal(await f.page.locator('.public-result-row').getByRole('button', { name: '下载 ZIP', exact: true }).isVisible(), true);
    assert.equal(f.downloads.length, 0);
  } finally { await f.context.close(); await browser.close(); }
});
test('healthy recovery in a fresh browser restores the same order and downloads, while unredeemed checks do not claim', async () => {
  const browser = await launch();
  try {
    for (const existingOrder of [true, false]) {
      const f = await fixture(browser, { existingOrder });
      try {
        await f.page.locator('.mantine-SegmentedControl-label').getByText('401 找回', { exact: true }).click();
        await f.page.getByRole('textbox', { name: '卡密', exact: true }).fill('fixture-card-a');
        await f.page.getByRole('button', { name: '检查并找回', exact: true }).click();
        await f.page.locator('.public-result-row').getByText('凭据正常', { exact: true }).waitFor();
        await f.page.waitForFunction(() => !document.querySelector('.public-input-panel button')?.hasAttribute('data-loading'));
        assert.equal(f.downloads.length, existingOrder ? 1 : 0);
        assert.equal(f.calls.filter(call => call.path.endsWith('/confirm')).length, existingOrder ? 1 : 0);
        assert.equal(f.calls.filter(call => call.path.endsWith('/reclaim')).length, 0);
        assert.equal(await f.page.getByText('待处理', { exact: true }).count(), 0);
      } finally { await f.context.close(); }
    }
  } finally { await browser.close(); }
});
