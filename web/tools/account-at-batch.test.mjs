import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { hasUsablePersonalAccess } from '../src/owner/accountSession.ts';
const runnerSource = (await readFile(new URL('../src/owner/accountATBatch.ts', import.meta.url), 'utf8')).replace("import { hasUsablePersonalAccess } from './accountSession';", '');
const { stripTypeScriptTypes } = await import('node:module');
const { AccountATBatchRunner } = await import(`data:text/javascript;base64,${Buffer.from(stripTypeScriptTypes(`const hasUsablePersonalAccess = ${hasUsablePersonalAccess.toString()};\n${runnerSource}`)).toString('base64')}`);

function account(id, overrides = {}) {
  return { id, identifier: `${id}@example.test`, status: 'active', hasPassword: true, hasTotp: true, ...overrides };
}

function ready(id) {
  return { targetAccountId: id, status: 'ready', checkedAt: new Date().toISOString() };
}

test('bulk AT runner pages through every account and counts skips without overfetching', async () => {
  const pages = new Map([
    [1, { total: 3, items: [account('one'), account('two', { status: 'disabled' }), account('three', { hasTotp: false })] }],
  ]);
  const listed = [];
  const acquired = [];
  const updates = [];
  const runner = new AccountATBatchRunner({
    listPage: async page => { listed.push(page); return pages.get(page) ?? { total: 3, items: [] }; },
    acquire: async id => { acquired.push(id); return ready(id); },
    readAccess: async id => ready(id),
  }, batch => updates.push(batch));

  await runner.run();

  const final = updates.at(-1);
  assert.deepEqual(listed, [1]);
  assert.deepEqual(acquired, ['one']);
  assert.deepEqual({ status: final.status, processed: final.processed, succeeded: final.succeeded, failed: final.failed, skipped: final.skipped }, {
    status: 'completed', processed: 3, succeeded: 1, failed: 0, skipped: 2,
  });
});

test('bulk AT runner reconciles an in-flight account before continuing after refresh', async () => {
  const startedAt = new Date(Date.now() - 5000).toISOString();
  const previous = {
    id: 'bulk-1', status: 'paused', total: 2, processed: 0, succeeded: 0, failed: 0, skipped: 0,
    page: 1, concurrency: 2, finished: [], accounts: [account('one'), account('two')], inFlight: [{ id: 'one', startedAt }],
  };
  const reads = [];
  const acquired = [];
  const updates = [];
  const runner = new AccountATBatchRunner({
    listPage: async () => ({ total: 2, items: previous.accounts }),
    acquire: async id => { acquired.push(id); return ready(id); },
    readAccess: async id => { reads.push(id); return ready(id); },
  }, batch => updates.push(batch));

  await runner.run(previous);

  const final = updates.at(-1);
  assert.deepEqual(reads, ['one']);
  assert.deepEqual(acquired, ['two']);
  assert.equal(final.status, 'completed');
  assert.equal(final.processed, 2);
});

test('bulk AT runner reuses current valid AT without another login request', async () => {
  const saved = { ...ready('one'), expiresAt: new Date(Date.now() + 30 * 86400000).toISOString() };
  const acquired = [];
  const updates = [];
  const runner = new AccountATBatchRunner({
    listPage: async () => ({ total: 1, items: [account('one', { personalAccess: saved })] }),
    acquire: async id => { acquired.push(id); return saved; },
    readAccess: async () => saved,
  }, batch => updates.push(batch));
  await runner.run();
  assert.deepEqual(acquired, []);
  assert.equal(updates.at(-1).succeeded, 1);
});

test('expired saved AT is acquired again instead of counted as already ready', async () => {
  const expired = { ...ready('one'), expiresAt: new Date(Date.now() - 1000).toISOString() };
  const acquired = []; const updates = [];
  const runner = new AccountATBatchRunner({
    listPage: async () => ({ total: 1, items: [account('one', { personalAccess: expired })] }),
    acquire: async id => { acquired.push(id); return ready(id); }, readAccess: async id => ready(id),
  }, batch => updates.push(batch));
  await runner.run(); assert.deepEqual(acquired, ['one']); assert.equal(updates.at(-1).succeeded, 1);
});

test('proxy preflight pause retains the exact account without inventing an in-flight request', async () => {
  const updates = [];
  const runner = new AccountATBatchRunner({
    listPage: async () => ({ total: 1, items: [account('one')] }),
    beforeAcquire: async () => '没有可用代理',
    acquire: async () => assert.fail('blocked account must not POST'), readAccess: async id => ready(id),
  }, batch => updates.push(batch));
  await runner.run(); const final = updates.at(-1);
  assert.equal(final.status, 'paused'); assert.equal(final.error, '没有可用代理'); assert.equal(final.processed, 0);
  assert.ok(!final.inFlight || Array.isArray(final.inFlight) && final.inFlight.length === 0);
  assert.equal(final.accounts[0].id, 'one');
});

test('a verifying acquisition is retained for reconciliation rather than counted as failed', async () => {
  const updates = [];
  const runner = new AccountATBatchRunner({
    listPage: async () => ({ total: 1, items: [account('one')] }),
    acquire: async id => ({ targetAccountId: id, status: 'verifying' }), readAccess: async id => ready(id),
  }, batch => updates.push(batch));
  await runner.run(); const final = updates.at(-1);
  assert.equal(final.status, 'paused'); assert.equal(final.processed, 0); assert.equal(final.failed, 0);
  assert.ok(final.inFlight);
});

test('bulk AT bounds requests and reconciles all lost responses before resuming', async () => {
  let active = 0; let maximum = 0; const acquired = []; const updates = [];
  let fail = true;
  const client = {
    listPage: async () => ({ total: 5, items: ['one', 'two', 'three', 'four', 'five'].map(id => account(id)) }),
    acquire: async id => {
      acquired.push(id); maximum = Math.max(maximum, ++active);
      await new Promise(resolve => setTimeout(resolve, 5)); active--;
      if (fail && ['one', 'two'].includes(id)) throw new Error('lost response');
      return ready(id);
    },
    readAccess: async id => ready(id),
  };
  const runner = new AccountATBatchRunner(client, batch => updates.push(batch));
  await runner.run(undefined, 2);
  assert.equal(maximum, 2);
  assert.equal(updates.at(-1).status, 'paused');
  assert.equal(updates.at(-1).inFlight.length, 2);
  fail = false;
  await runner.run(updates.at(-1), 2);
  assert.equal(updates.at(-1).status, 'completed');
  assert.equal(updates.at(-1).succeeded, 5);
  assert.equal(new Set(acquired).size, acquired.length);
});

test('stop lets active requests finish and starts no further account', async () => {
  const updates = []; let runner; const acquired = [];
  runner = new AccountATBatchRunner({
    listPage: async () => ({ total: 4, items: ['one','two','three','four'].map(id => account(id)) }),
    acquire: async id => { acquired.push(id); await new Promise(resolve => setTimeout(resolve, 5)); runner.stop(); return ready(id); },
    readAccess: async id => ready(id),
  }, batch => updates.push(batch));
  await runner.run(undefined, 2);
  assert.equal(acquired.length, 2);
  assert.equal(updates.at(-1).status, 'canceled');
  assert.equal(updates.at(-1).processed, 2);
});


test('resume preserves the saved task concurrency after a control change or refresh', async () => {
 let maximum=0;let active=0;const updates=[];
 const previous={id:'resume',status:'paused',concurrency:2,total:4,processed:0,succeeded:0,failed:0,skipped:0,page:1,accounts:['one','two','three','four'].map(id=>account(id)),finished:[],inFlight:[]};
 const runner=new AccountATBatchRunner({listPage:async()=>assert.fail('saved page is retained'),readAccess:async id=>ready(id),acquire:async id=>{maximum=Math.max(maximum,++active);await new Promise(resolve=>setTimeout(resolve,5));active--;return ready(id);}},batch=>updates.push(batch));
 await runner.run(previous,1);assert.equal(maximum,2);assert.equal(updates.at(-1).concurrency,2);assert.equal(updates.at(-1).status,'completed');
});


test('a reduced global ceiling constrains resumed work without rewriting its saved limit', async () => {
 let active=0;let maximum=0;const updates=[];
 const previous={id:'resume',status:'paused',concurrency:3,total:2,processed:0,succeeded:0,failed:0,skipped:0,page:1,accounts:['one','two'].map(id=>account(id)),finished:[],inFlight:[]};
 const runner=new AccountATBatchRunner({listPage:async()=>assert.fail('retain page'),readAccess:async id=>ready(id),acquire:async id=>{maximum=Math.max(maximum,++active);await new Promise(resolve=>setTimeout(resolve,5));active--;return ready(id);}},batch=>updates.push(batch));
 await runner.run(previous,1,1);assert.equal(maximum,1);assert.equal(updates.at(-1).concurrency,3);assert.equal(updates.at(-1).status,'completed');
});
