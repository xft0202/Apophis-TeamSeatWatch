// Real registered Owner API, Public gateway and compiled candidate; external
// platform/channel adapters are typed mocks supplied by the runtime fixture.
import { chromium } from '/root/.local/share/playwright-cli/node_modules/playwright/index.mjs';
import assert from 'node:assert/strict';
import { mkdir, writeFile } from 'node:fs/promises';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
const [ownerURL, publicURL, password] = process.argv.slice(2);
const evidence = process.env.TSW_BROWSER_EVIDENCE;
const browser = await chromium.launch({ executablePath: '/root/.cache/ms-playwright/chromium-1243/chrome-linux64/chrome', headless: true, args: ['--no-sandbox'] });
const context = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1440, height: 1000 }, acceptDownloads: true });
const page = await context.newPage();
page.setDefaultTimeout(10_000);
const errors = [];
const network = [];
page.on('response', (response) => { if (response.status() >= 400 && response.url().includes('/api/')) network.push({ url: response.url(), status: response.status() }); });
page.on('requestfailed', (request) => network.push({ url: request.url(), error: request.failure()?.errorText }));
page.on('pageerror', (error) => errors.push(error.message));
const button = (name) => page.getByRole('button', { name, exact: true });
const snapshots = [];
async function inspect(name) {
  const observation = await page.evaluate(() => {
    const visible = [...document.querySelectorAll('main *')].filter((e) => e.getBoundingClientRect().height > 0 && [...e.childNodes].some((n) => n.nodeType === Node.TEXT_NODE && n.textContent.trim()));
    const weights = [...new Set(visible.map((e) => getComputedStyle(e).fontWeight))].sort();
    const primary = getComputedStyle(document.documentElement).getPropertyValue('--mantine-color-indigo-5').trim();
    return { weights, primary, background: getComputedStyle(document.body).backgroundColor, overflow: document.documentElement.scrollWidth > innerWidth };
  });
  assert.deepEqual(observation.weights, ['400', '600'], name);
  assert.equal(observation.primary.toLowerCase(), '#6366f1');
  assert.equal(observation.background, 'rgb(250, 250, 250)');
  assert.equal(observation.overflow, false, name);
  snapshots.push({ name, ...observation });
  if (evidence) { await mkdir(evidence, { recursive: true }); await page.screenshot({ path: `${evidence}/${name}.png`, fullPage: true }); }
}
try {
  await page.goto(`${ownerURL}/owner/`);
  await page.getByRole('textbox', { name: '用户名', exact: true }).fill('owner');
  await page.getByPlaceholder('输入密码', { exact: true }).fill(password);
  await page.getByPlaceholder('输入密码', { exact: true }).press('Tab');
  assert.equal(await button('登录').evaluate((e) => e === document.activeElement), true);
  await page.keyboard.press('Enter');
  await page.getByRole('heading', { name: 'Owner 控制台' }).waitFor();
  await inspect('owner-mother-desktop');
  // Cross-page exact selection and immutable range/count are real table/API work.
  await page.getByRole('tab', { name: '子号资料', exact: true }).click();
  const search = page.getByRole('textbox', { name: '搜索邮箱或域名', exact: true });
  await search.fill('@selection.test');
  await page.getByText('共 21 条 · 已选 0 条 · 第 1 页', { exact: true }).waitFor();
  await page.getByRole('checkbox', { name: '选择本页', exact: true }).check();
  await button('2').click();
  await page.getByText('共 21 条 · 已选 20 条 · 第 2 页', { exact: true }).waitFor();
  await page.getByRole('checkbox', { name: '选择本页', exact: true }).check();
  await page.getByRole('checkbox', { name: '确认导出已选跨页 21 条', exact: true }).check();
  const exportDownload = page.waitForEvent('download'); await button('导出 TXT').click();
  assert.match((await exportDownload).suggestedFilename(), /\.txt$/);
  await inspect('owner-table-desktop');
  // An invalid TXT reports the actual bad line and keeps the user's input.
  const source = page.getByRole('textbox', { name: '子号资料文本', exact: true });
  await page.locator('input[type=file]').setInputFiles({ name: 'invalid.txt', mimeType: 'text/plain', buffer: Buffer.from('invalid row without fields') });
  await button('保存资料').click();
  await page.getByText('已保存 0 · 重复 0 · 无效 1', { exact: true }).waitFor();
  assert.equal(await source.inputValue(), 'invalid row without fields');
  // Reject the genuine registered mutation's CSRF header; the draft stays put.
  await page.route('**/api/owner/v1/child-materials/import', async (route) => { const headers = { ...route.request().headers(), 'x-csrf-token': 'invalid' }; await route.continue({ headers }); });
  await button('保存资料').click();
  await page.getByText('没有保存资料的权限。', { exact: true }).waitFor();
  assert.equal(await source.inputValue(), 'invalid row without fields');
  await page.unroute('**/api/owner/v1/child-materials/import');
  await page.setViewportSize({ width: 390, height: 844 });
  await search.focus();
  assert.equal(await search.evaluate((e) => e === document.activeElement), true);
  await inspect('owner-table-narrow');
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.getByRole('tab', { name: '开始操作', exact: true }).click();
  await page.getByRole('radio', { name: '演示母号', exact: true }).check();
  await button('确认母号，下一步').click();
  await page.getByRole('radio', { name: '演示空间', exact: true }).check();
  await button('明确确认空间，下一步').click();
  await page.getByRole('radio', { name: '演示批次', exact: true }).check();
  await page.getByRole('checkbox', { name: 'candidate1@remove.test · 资料已保存', exact: true }).check();
  await button('确认 1 个子号，下一步').click();
  await page.getByRole('radio', { name: '演示渠道 · 目标组 42', exact: true }).check();
  await button('确认去向，查看选择').click();
  await button('预览到期换批').click();
  await page.getByRole('combobox', { name: 'old1@remove.test', exact: true }).click();
  await page.getByRole('option', { name: 'candidate1@remove.test', exact: true }).click();
  await inspect('owner-confirm-desktop');
  await button('确认本轮席位与候选').focus();
  assert.equal(await button('确认本轮席位与候选').evaluate((e) => e === document.activeElement), true);
  await page.keyboard.press('Enter');
  await button('建立逐席清退进度（尚不清退）').click();
  await button('清退此原成员').click();
  await page.getByText('空位已核实', { exact: true }).waitFor();
  // A refresh restores the original authorized task without another mutation.
  const mutations = [];
  page.on('request', (r) => { if (r.method() !== 'GET' && r.url().includes('/api/owner/')) mutations.push(r.url()); });
  await page.reload();
  await button('执行原义务').waitFor();
  assert.deepEqual(mutations, []);
  // Saved task recovery is independent of every supplementary label read.
  const savedDraft = await (await context.request.get(`${ownerURL}/api/owner/v1/operation-draft`)).json();
  const savedPreview = await (await context.request.get(`${ownerURL}/api/owner/v1/expiry-rotation/previews/latest`)).json();
  const labelFailures = [
    [/\/api\/owner\/v1\/mother-accounts(?:\?|$)/, '母号名称暂时无法读取，已保留当前任务。'],
    [/\/api\/owner\/v1\/standby-child-batches$/, '批次名称暂时无法读取，已保留当前任务。'],
    [/\/api\/owner\/v1\/delivery-destinations$/, '去向名称暂时无法读取，已保留当前任务。'],
    [/\/api\/owner\/v1\/mother-accounts\/[^/]+\/discovery$/, '空间名称暂时无法读取，已保留当前任务。'],
  ];
  for (const [path, notice] of labelFailures) {
    await page.route(path, (route) => route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ code: 'delivery_unavailable' }) }));
    await page.reload();
    await page.getByText(notice, { exact: true }).waitFor();
    await button('执行原义务').waitFor();
    await page.getByText('已确认 1 个席位', { exact: true }).waitFor();
    await page.unroute(path);
  }
  // A destination 401 is an authentication boundary, never a label fallback.
  const destinationPath = /\/api\/owner\/v1\/delivery-destinations$/;
  await page.route(destinationPath, (route) => route.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ code: 'session_expired' }) }));
  await button('刷新当前步骤').click();
  await page.getByText('登录已过期，请重新登录。', { exact: true }).waitFor();
  await page.unroute(destinationPath);
  await button('刷新当前步骤').click();
  await page.getByText('登录已过期，请重新登录。', { exact: true }).waitFor({ state: 'hidden' });
  await page.getByText('正在读取草案…', { exact: true }).waitFor({ state: 'hidden' });
  await button('执行原义务').waitFor();
  assert.deepEqual(await (await context.request.get(`${ownerURL}/api/owner/v1/operation-draft`)).json(), savedDraft);
  assert.equal((await (await context.request.get(`${ownerURL}/api/owner/v1/expiry-rotation/previews/latest`)).json()).id, savedPreview.id);
  assert.deepEqual(mutations, []);
  await button('执行原义务').click();
  await button('核实原空间成员').click();
  await button('保存空间凭据').click();
  await button('核实首次用量').click();
  await page.getByText('当前窗口为零', { exact: true }).waitFor();
  await button('生成交付包').click();
  await button('生成卡密').waitFor();
  await inspect('owner-progress-desktop');
  await page.setViewportSize({ width: 390, height: 844 });
  await inspect('owner-progress-narrow');
  await page.setViewportSize({ width: 1440, height: 1000 });
  const zipDownload = page.waitForEvent('download'); await page.getByRole('link', { name: '下载原交付包', exact: true }).click();
  const filename = (await zipDownload).suggestedFilename();
  await button('生成卡密').click();
  const card = await page.getByRole('textbox', { name: '卡密', exact: true }).inputValue();
  const expiry = (hours) => new Date(Date.now() + hours * 3600000).toISOString().slice(0, 16);
  await page.getByLabel('首次领取截止时间', { exact: true }).fill(expiry(1));
  await page.getByLabel('原交付找回截止时间', { exact: true }).fill(expiry(2));
  await page.getByRole('checkbox', { name: '已保存卡密，确认开放本包的卡密领取', exact: true }).check();
  await button('激活卡密领取').click();
  await page.getByText('已激活', { exact: true }).waitFor();
  const { stdout } = await promisify(execFile)(process.execPath, ['../../web/tools/public-browser.mjs', publicURL, card, filename], { env: process.env });
  console.log(stdout.trim());
  await button('退出登录').click();
  await page.getByRole('heading', { name: 'Owner 登录' }).waitFor();
  const denied = await context.request.get(`${ownerURL}/api/owner/v1/operation-draft`);
  assert.equal(denied.status(), 401);
  assert.deepEqual(errors, []);
  const report = { browser: browser.version(), registeredOwnerAndPublic: true, compiledCandidate: true, snapshots, cases: ['password login with keyboard', 'precise cross-page selection and TXT export', 'invalid TXT upload', 'registered CSRF rejection preserves input', 'explicit mother/workspace/batch/account/destination', 'explicit compatible slot confirmation', 'per-slot progress', 'refresh original task without mutation', 'four failed label reads preserve exact draft and original authorized task', 'destination 401 retains authentication boundary', 'membership/credentials/first usage', 'one original ZIP', 'independent Public claim/recovery', 'logout denies Owner'], platform: 'mock only', manualAcceptance: 'pending user' };
  if (evidence) await writeFile(`${evidence}/owner-browser-report.json`, `${JSON.stringify(report, null, 2)}\n`);
  console.log(JSON.stringify(report));
} catch (error) {
  if (evidence) { await mkdir(evidence, { recursive: true }); await page.screenshot({ path: `${evidence}/owner-failure.png`, fullPage: true }); await writeFile(`${evidence}/owner-failure.html`, await page.content()); await writeFile(`${evidence}/owner-failure-state.json`, JSON.stringify({ errors, network, snapshot: await page.locator('body').ariaSnapshot() }, null, 2)); }
  throw error;
} finally { await context.close(); await browser.close(); }
