import type { components } from '../generated/owner';
import { hasUsablePersonalAccess } from './accountSession';

type Account = components['schemas']['TargetAccount'];
type Access = components['schemas']['TargetPersonalAccess'];
export type ATBatchAccount = Pick<Account, 'id' | 'identifier' | 'status' | 'hasPassword' | 'hasTotp' | 'personalAccess'>;
export type ATBatch = {
  id: string;
  status: 'running' | 'paused' | 'completed' | 'canceled';
  concurrency: number;
  total: number | null;
  processed: number;
  succeeded: number;
  failed: number;
  skipped: number;
  page: number;
  accounts: ATBatchAccount[];
  finished: string[];
  inFlight: { id: string; startedAt: string }[];
  error?: string;
};

type Client = {
  listPage: (page: number) => Promise<{ items: ATBatchAccount[]; total: number }>;
  beforeAcquire?: (id: string) => Promise<string | undefined>;
  acquire: (id: string) => Promise<Access>;
  readAccess: (id: string) => Promise<Access>;
};

function errorStatus(error: unknown): number | undefined {
  if (error && typeof error === 'object' && 'status' in error && typeof error.status === 'number') return error.status;
}

export function restoreATBatch(raw: string | null): ATBatch | null {
  if (!raw) return null;
  try {
    const batch: ATBatch = JSON.parse(raw);
    const counters = [batch.processed, batch.succeeded, batch.failed, batch.skipped, batch.page];
    if (typeof batch.id !== 'string' || !['running', 'paused', 'completed', 'canceled'].includes(batch.status) ||
        !Number.isSafeInteger(batch.concurrency) || batch.concurrency < 1 || batch.concurrency > 100 ||
        !counters.every(value => Number.isSafeInteger(value) && value >= 0) || batch.page < 1 ||
        !(batch.total === null || Number.isSafeInteger(batch.total) && batch.total >= batch.processed) ||
        batch.processed !== batch.succeeded + batch.failed + batch.skipped ||
        !Array.isArray(batch.accounts) || batch.accounts.length > 100 ||
        !batch.accounts.every(account => typeof account.id === 'string' && typeof account.identifier === 'string' &&
          ['active', 'disabled'].includes(account.status) && typeof account.hasPassword === 'boolean' && typeof account.hasTotp === 'boolean') ||
        !Array.isArray(batch.finished) || !Array.isArray(batch.inFlight)) return null;
    const accountIds = new Set(batch.accounts.map(account => account.id));
    if (accountIds.size !== batch.accounts.length || new Set(batch.finished).size !== batch.finished.length ||
        batch.finished.some(id => !accountIds.has(id)) || new Set(batch.inFlight.map(item => item.id)).size !== batch.inFlight.length ||
        batch.inFlight.some(item => !accountIds.has(item.id) || batch.finished.includes(item.id) || !Number.isFinite(Date.parse(item.startedAt)))) return null;
    return batch.status === 'running' ? { ...batch, status: 'paused' } : batch;
  } catch { return null; }
}

// Owns bounded paging, concurrent session mutations and recovery of every lost response.
export class AccountATBatchRunner {
  private stopRequested = false;
  private pauseRequested = false;
  private running = false;
  private readonly client: Client;
  private readonly update: (batch: ATBatch) => void;
  constructor(client: Client, update: (batch: ATBatch) => void) { this.client = client; this.update = update; }

  stop() { this.stopRequested = true; }
  pause() { this.pauseRequested = true; }

  async run(previous?: ATBatch, concurrency = 1, capacity = 100): Promise<void> {
    if (this.running) return;
    this.running = true; this.stopRequested = false; this.pauseRequested = false;
    concurrency = previous?.concurrency ?? Math.max(1, Math.min(100, Math.trunc(concurrency)));
    let batch: ATBatch = previous ? { ...previous, status: 'running', concurrency } : {
      id: crypto.randomUUID(), status: 'running', concurrency, total: null, processed: 0,
      succeeded: 0, failed: 0, skipped: 0, page: 1, accounts: [], finished: [], inFlight: [],
    };
    delete batch.error;
    const executionLimit = Math.min(concurrency, Math.max(1, Math.min(100, capacity)));
    const publish = () => this.update({ ...batch, accounts: [...batch.accounts], finished: [...batch.finished], inFlight: [...batch.inFlight] });
    const pause = (message: string) => { this.pauseRequested = true; batch = { ...batch, error: message }; publish(); };
    const finish = (id: string, outcome: 'succeeded' | 'failed' | 'skipped') => {
      batch = { ...batch, [outcome]: batch[outcome] + 1, processed: batch.processed + 1,
        finished: [...batch.finished, id], inFlight: batch.inFlight.filter(item => item.id !== id) };
      publish();
    };
    const process = async (account: ATBatchAccount, recovery = false) => {
      try {
        if (account.status !== 'active') { finish(account.id, 'skipped'); return; }
        if (!recovery && hasUsablePersonalAccess(account.personalAccess)) { finish(account.id, 'succeeded'); return; }
        if (!account.hasPassword || !account.hasTotp) { finish(account.id, 'skipped'); return; }
        let access: Access | undefined;
        if (recovery) {
          const pending = batch.inFlight.find(item => item.id === account.id)!;
          try { access = await this.client.readAccess(account.id); }
          catch (error: unknown) { if (errorStatus(error) === 404) { finish(account.id, 'failed'); return; } throw error; }
          if (access.status === 'verifying') { pause('上次获取仍在处理中，稍后继续'); return; }
          if (!access.checkedAt || Date.parse(access.checkedAt) < Date.parse(pending.startedAt) - 1000) access = undefined;
        }
        if (!access) {
          const blocked = await this.client.beforeAcquire?.(account.id);
          if (blocked) { pause(blocked); return; }
          if (this.stopRequested || this.pauseRequested) return;
          batch = { ...batch, inFlight: [...batch.inFlight.filter(item => item.id !== account.id), { id: account.id, startedAt: new Date().toISOString() }] };
          publish();
          access = await this.client.acquire(account.id);
        }
        if (access.status === 'verifying') { pause('获取仍在处理中，稍后继续'); return; }
        finish(account.id, access.status === 'ready' ? 'succeeded' : 'failed');
      } catch (error: unknown) {
        const status = errorStatus(error);
        if (status === 404 || status === 422) finish(account.id, 'failed');
        else pause(status === 401 ? '管理端登录已过期，请登录后继续' : status === 403 ? '没有获取权限' : '获取结果待确认，请继续获取');
      }
    };
    publish();
    try {
      // Reconcile the saved in-flight attempts before starting any fresh account.
      for (let offset = 0; offset < batch.inFlight.length && !this.pauseRequested;) {
        const recovered = batch.inFlight.slice(offset, offset + executionLimit);
        await Promise.all(recovered.map(item => process(batch.accounts.find(account => account.id === item.id)!, true)));
        if (recovered.some(item => batch.inFlight.some(pending => pending.id === item.id))) break;
        offset = 0;
      }
      while (!this.stopRequested && !this.pauseRequested) {
        if (batch.total !== null && batch.processed >= batch.total) break;
        if (!batch.accounts.length) {
          const result = await this.client.listPage(batch.page);
          if (batch.total !== null && batch.total !== result.total) { pause('账号数量已变化，请重新开始获取'); break; }
          if (!result.items.length && result.total > batch.processed) throw new Error('account_page_changed');
          batch = { ...batch, total: result.total, accounts: result.items.map(({ id, identifier, status, hasPassword, hasTotp, personalAccess }) => ({ id, identifier, status, hasPassword, hasTotp, ...(personalAccess ? { personalAccess } : {}) })) };
          publish();
          if (!result.items.length) break;
        }
        const queue = batch.accounts.filter(account => !batch.finished.includes(account.id));
        let cursor = 0;
        await Promise.all(Array.from({ length: Math.min(executionLimit, queue.length) }, async () => {
          while (!this.stopRequested && !this.pauseRequested && cursor < queue.length) await process(queue[cursor++]!);
        }));
        if (batch.finished.length === batch.accounts.length) {
          batch = { ...batch, page: batch.page + 1, accounts: [], finished: [], inFlight: [] }; publish();
        }
      }
      batch = { ...batch, status: batch.total !== null && batch.processed >= batch.total ? 'completed' : this.stopRequested ? 'canceled' : 'paused' };
      publish();
    } catch (error: unknown) {
      batch = { ...batch, status: 'paused', error: errorStatus(error) === 401 ? '管理端登录已过期，请登录后继续' : '账号读取失败，请继续获取' };
      publish();
    } finally { this.running = false; }
  }
}
