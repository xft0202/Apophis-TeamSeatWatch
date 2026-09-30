import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { URL } from 'node:url';

const accountPage = new URL('../src/owner/AccountsPage.tsx', import.meta.url);
const inkCss = new URL('../src/owner/ink.css', import.meta.url);

test('account import stays inline, non-blocking, and preserves its draft across tab changes', async () => {
  const [source, css] = await Promise.all([
    readFile(accountPage, 'utf8'),
    readFile(inkCss, 'utf8'),
  ]);

  assert.doesNotMatch(source, /<Modal\b|\bModal\s*[,}]/, 'a blocking modal must not return');
  assert.match(source, /aria-controls="account-import-panel"/);
  assert.match(source, /<section id="account-import-panel"/);
  assert.match(source, /setKind\('mother'\);\s*setImportOpen\(false\);\s*importPreview\.reset\(\);/s);
  assert.equal((source.match(/setImportText\(''\)/g) ?? []).length, 1,
    'only a successful import clears the draft; tab switches and cancel preserve it');
  assert.match(source, /member@example\.test----example-password----JBSWY3DPEHPK3PXP/,
    'documentation uses an unmistakably fake account');

  for (const selector of [
    '.account-import-panel',
    '.account-import-panel__head',
    '.account-import-panel__actions',
    '.account-import-panel__preview',
  ]) {
    assert.match(css, new RegExp(selector.replaceAll('.', '\\.')));
  }
});
