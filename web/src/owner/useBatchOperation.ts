import { useEffect, useRef, useState } from 'react';
import { ownerProblem } from './auth';
import { batchOperationsApi, type Batch, type BatchDetail, type DeliveryList, type JoinOperation, type JoinPreview } from './batchOperations';
import { operationProgress } from './operationWizardState';
import { acknowledgeOperationIntent, operationIntent } from './operationIntent';
import { accountCardIntent, saveAccountCard } from './accountCards';
import { cardManagementApi } from './cardManagement';
import useTaskConcurrency from './useTaskConcurrency';
import { operationActionFailure } from './operationWizardState';
import { formatDateTimeInput } from '../shared/dateTime';

export function defaultPlannedAt() { return formatDateTimeInput(new Date(Date.now() + 7 * 86400000).toISOString()); }

export function useBatchOperation(active: boolean, onNotice: (message: string) => void) {
  const taskConcurrency = useTaskConcurrency(active);
  const [batchId, setBatchId] = useState<string | null>(null);
  const [detail, setDetail] = useState<BatchDetail | null>(null);
  const [joinPreview, setJoinPreview] = useState<JoinPreview | null>(null);
  const [joinOperation, setJoinOperation] = useState<JoinOperation | null>(null);
  const [deliveries, setDeliveries] = useState<DeliveryList | null>(null);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [revision, setRevision] = useState(0);
  const [loading, setLoading] = useState(false);
  const [pending, setPending] = useState('');
  const [cardProgress, setCardProgress] = useState<{ done: number; total: number } | null>(null);
  const [rowErrors, setRowErrors] = useState<Record<string, string>>({});
  const locked = useRef(false);
  const scope = useRef(0);
  const notice = useRef(onNotice); notice.current = onNotice;

  function open(id: string | null) {
    if (locked.current || id === batchId) return;
    scope.current++; setBatchId(id); setDetail(null); setJoinOperation(null); setJoinPreview(null); setDeliveries(null); setPage(1); setLoading(Boolean(id)); setRowErrors({}); setCardProgress(null);
  }
  function refresh() { setRevision((value) => value + 1); }
  function accept(batch: Batch) { setBatchId(batch.id); setDetail(null); setPage(1); setLoading(true); refresh(); }

  useEffect(() => {
    if (!active || !batchId || pending) return;
    const controller = new AbortController();
    const signal = controller.signal;
    setLoading(true);
    void Promise.all([
      batchOperationsApi.batch(batchId, page, pageSize, signal),
      batchOperationsApi.operation(batchId, page, pageSize, signal).catch((error: unknown) => { if (ownerProblem(error).status === 404) return null; throw error; }),
      batchOperationsApi.deliveries(batchId, page, pageSize, signal),
    ]).then(async ([nextDetail, invitation, logins]) => {
      const preview = nextDetail.batch.status === 'ended' ? null : await batchOperationsApi.preview(batchId, signal);
      if (signal.aborted) return;
      const lastPage = Math.max(1, Math.ceil(nextDetail.targetTotal / pageSize));
      if (page > lastPage) { setPage(lastPage); return; }
      setDetail(nextDetail); setJoinOperation(invitation); setDeliveries(logins); setJoinPreview(preview);
    }).catch((error: unknown) => {
      if (!signal.aborted) notice.current(ownerProblem(error).status === 401 ? '登录已失效，请重新登录。' : '本轮进度读取失败，请刷新重试。');
    }).finally(() => { if (!signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [active, batchId, page, pageSize, revision, pending]);

  const operationBatch = detail?.batch ?? null;
  const progress = operationProgress(operationBatch, joinOperation, deliveries);
  const waiting = Boolean(joinOperation?.activeTaskCount);
  useEffect(() => {
    if (!active || !batchId || !waiting || pending || loading) return;
    const timer = window.setTimeout(refresh, 3000);
    return () => window.clearTimeout(timer);
  }, [active, batchId, waiting, pending, loading, revision]);

  async function action(name: string, execute: () => Promise<unknown>, failure: string) {
    if (locked.current || !batchId) return;
    locked.current = true; const current = scope.current; setPending(name); notice.current('');
    try { await execute(); }
    catch (error: unknown) { if (current === scope.current) notice.current(operationActionFailure(ownerProblem(error), failure)); }
    finally { locked.current = false; if (current === scope.current) { setPending(''); refresh(); } }
  }

  async function invite() {
    if (!operationBatch || !joinPreview?.canProceed || joinOperation) return;
    await action('invite', () => batchOperationsApi.startJoin(operationBatch.id, `join:${operationBatch.id}`, taskConcurrency.concurrency), '邀请启动结果待确认，请刷新本轮记录。');
  }
  async function continueInvitation(kind: 'retry' | 'reconcile') {
    if (!batchId || !joinOperation) return;
    const id = batchId;
    const intent = `${kind}:${id}`;
    await action(kind, async () => {
      if (kind === 'retry') await batchOperationsApi.retryJoin(id, operationIntent(intent));
      else await batchOperationsApi.reconcileJoin(id, operationIntent(intent));
      acknowledgeOperationIntent(intent);
    }, '处理结果待确认，请刷新本轮记录。');
  }
  async function login(targetAccountId?: string) {
    if (!batchId || !progress.invited || !['joining', 'serving'].includes(operationBatch?.status ?? '')) return;
    const id = batchId; const intent = `login:${id}:${targetAccountId ?? 'all'}`;
    await action(`login:${targetAccountId ?? 'all'}`, async () => {
      const result = await batchOperationsApi.login(id, operationIntent(intent), targetAccountId, taskConcurrency.concurrency);
      if (!result.queued && joinOperation?.blockedCount) notice.current('当前任务正在核验，请稍后刷新本轮进度。');
      acknowledgeOperationIntent(intent);
    }, 'OAuth 登录启动结果待确认，请刷新本轮进度。');
  }
  function cardSaved(membershipId: string, activation: Awaited<ReturnType<typeof saveAccountCard>>['activation']) {
    const previous = joinOperation?.targets.find((target) => target.delivery?.membershipId === membershipId)?.delivery;
    const saved = { cardStatus: activation.status, cardActivated: true, cardDisplaySuffix: activation.displaySuffix, redemptionDeadline: activation.redemptionDeadline };
    setJoinOperation((current) => current ? { ...current, targets: current.targets.map((target) => target.delivery?.membershipId === membershipId ? { ...target, delivery: { ...target.delivery, ...saved } } : target) } : current);
    setDeliveries((current) => {
      if (!current) return current;
      return { ...current, cardCount: previous?.cardStatus ? current.cardCount : Math.min(current.total, current.cardCount + 1), items: current.items.map((item) => item.membershipId === membershipId ? { ...item, ...saved } : item) };
    });
    setRowErrors((errors) => { const next = { ...errors }; delete next[membershipId]; return next; });
  }
  async function generateCard(membershipId: string) {
    if (!['joining', 'serving'].includes(operationBatch?.status ?? '') || joinOperation?.targets.find((target) => target.delivery?.membershipId === membershipId)?.delivery?.status !== 'ready') return;
    await action(`card:${membershipId}`, async () => {
      try { const result = await saveAccountCard(membershipId); cardSaved(membershipId, result.activation); }
      catch (error: unknown) { setRowErrors((errors) => ({ ...errors, [membershipId]: '保存结果待确认，点击重试使用原卡密。' })); throw error; }
    }, '卡密保存结果待确认，重试会继续原卡密。');
  }
  async function generateAllCards() {
    if (!batchId || !progress.canGenerateCards || !['joining', 'serving'].includes(operationBatch?.status ?? '')) return;
    const id = batchId;
    await action('cards', async () => {
      const total = Math.max(0, (deliveries?.readyCount ?? 0) - (deliveries?.cardCount ?? 0));
      let done = 0;
      setCardProgress({ done, total });
      for (let currentPage = 1; ; currentPage++) {
        const records = await batchOperationsApi.deliveries(id, currentPage, 100);
        const todo = records.items.filter((item) => {
          const intent = accountCardIntent(item.membershipId);
          return item.status === 'ready' && (!item.cardStatus || Boolean(intent && !intent.saved));
        });
        for (let offset = 0; offset < todo.length; offset += 4) {
          await Promise.all(todo.slice(offset, offset + 4).map(async (item) => {
            try { const result = await saveAccountCard(item.membershipId); cardSaved(item.membershipId, result.activation); }
            catch { setRowErrors((errors) => ({ ...errors, [item.membershipId]: '保存结果待确认，点击重试使用原卡密。' })); }
            done++; setCardProgress({ done, total: Math.max(total, done) });
          }));
        }
        if (currentPage * records.pageSize >= records.total) break;
      }
    }, '卡密批量处理暂停，已保存项保留，点击继续处理。');
  }

  async function exportCards() {
    if (!batchId || locked.current) return;
    const id = batchId;
    await action('export', async () => {
      const current = operationBatch;
      if (!current?.motherAccountId) { notice.current('当前批次缺少母号信息，无法导出卡密。'); return; }
      const exported: string[] = []; let omitted = 0;
      for (let currentPage = 1; ; currentPage++) {
        const records = await cardManagementApi.list({ mother_account_id: current.motherAccountId, workspace_id: current.workspaceId, batch_id: id, page: currentPage, page_size: 100, cards_only: true });
        const items = records.items.filter((item) => ['unclaimed', 'claimed'].includes(item.cardState) && item.cardVersion !== undefined);
        if (items.length) {
          const result = await cardManagementApi.export(current.motherAccountId, current.workspaceId, items.map((item) => ({ membershipId: item.membershipId, cardVersion: item.cardVersion! })));
          exported.push(...result.items.map((item) => `${item.targetIdentifier}----${item.cardSecret}`));
          omitted += result.unavailable.length;
        }
        if (currentPage * records.pageSize >= records.total) break;
      }
      if (!exported.length) { notice.current(omitted ? `当前批次有 ${omitted} 张卡密无法读取完整内容。` : '当前批次没有可导出的卡密。'); return; }
      const url = URL.createObjectURL(new Blob([exported.join('\n') + '\n'], { type: 'text/plain;charset=utf-8' }));
      const link = document.createElement('a'); link.href = url; link.download = `${operationBatch?.sourceBatchName ?? '本轮'}-卡密.txt`; link.click(); URL.revokeObjectURL(url);
      notice.current(`已导出 ${exported.length} 张卡密${omitted ? `，${omitted} 张无法读取完整内容` : ''}。`);
    }, '卡密导出失败，请重试。');
  }

  return { taskConcurrency, batchId, operationBatch, detail, joinPreview, joinOperation, deliveries, progress, page, pageSize, loading, pending, cardProgress, rowErrors, open, accept, refresh, invite, continueInvitation, login, generateCard, generateAllCards, exportCards, setPage, setPageSize: (size: number) => { setPageSize(size); setPage(1); } };
}
export type BatchOperationState = ReturnType<typeof useBatchOperation>;
