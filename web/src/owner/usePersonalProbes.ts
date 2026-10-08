import { useActionFeedback } from '../shared/useActionFeedback';
import { useEffect, useRef, useState } from 'react';
import type { components } from '../generated/owner';
import { getTargetPersonalAccess, cancelPersonalProbes, createPersonalProbes, getPersonalProbes, getPersonalProbesByRequest, ownerProblem, previewPersonalProbes } from './auth';

import { accountNetworkPrerequisite } from './accountActionPrerequisites';
import { hasUsablePersonalAccess, type AccountFeedback } from './accountSession';

type Account = components['schemas']['TargetAccount'];
type Batch = components['schemas']['PersonalProbeBatch'];
type Scope = components['schemas']['PersonalProbeScope'];
const storageKey = 'owner-personal-probe-batch';
const pendingStorageKey = 'owner-personal-probe-pending-request';
const pendingIdsStorageKey = 'owner-personal-probe-pending-accounts';

function restoredIds(): string[] {
  try {
    const value: unknown = JSON.parse(sessionStorage.getItem(pendingIdsStorageKey) ?? '[]');
    return Array.isArray(value) ? value.filter((id): id is string => typeof id === 'string') : [];
  } catch { return []; }
}

export function usePersonalProbes(onResults: () => void, concurrency = 1) {
  const [batch, setBatch] = useState<Batch | null>(null);
  const [batchId, setBatchId] = useState(() => sessionStorage.getItem(storageKey));
  const [requestKey, setRequestKey] = useState(() => sessionStorage.getItem(pendingStorageKey));
  const [startingIds, setStartingIds] = useState<ReadonlySet<string>>(() => new Set(restoredIds()));
  const [starting, setStarting] = useState(false);
  const [canceling, setCanceling] = useState(false);
  const [notice, setNotice, dismissNotice] = useActionFeedback('');
  const [feedback, setFeedback] = useActionFeedback<ReadonlyMap<string, AccountFeedback>>(new Map());
  const [disconnected, setDisconnected] = useState(false);
  const startLock = useRef(false);
  const completedId = useRef<string | null>(null);
  const savedProgress = useRef({ id: '', count: 0 });
  const onResultsRef = useRef(onResults);
  onResultsRef.current = onResults;
  const active = !!batch && batch.queued + batch.running + batch.notSavedOrRetained > 0;
  const busy = starting || !!requestKey || active || (!!batchId && !batch);

  function clearPending() {
    sessionStorage.removeItem(pendingStorageKey);
    sessionStorage.removeItem(pendingIdsStorageKey);
    setRequestKey(null); setStartingIds(new Set());
  }

  function accept(result: Batch) {
    setBatch(result); setDisconnected(false);
    setFeedback(current => {
      const next = new Map(current);
      for (const item of result.items) {
        if (item.status === 'queued' || item.status === 'running') next.set(item.targetAccountId, { updatedAt: Date.now(), message: '探测中', tone: 'warning' });
        else if (item.outcome) {
          const messages = { available: '探测完成，账号正常', missing_personal_credential: '未获取 AT，请先获取 AT 后探测', credential_invalid: 'AT 已失效，请重新获取 AT', forbidden: '平台拒绝访问，请核对账号权限', banned: '平台确认账号已停用', network_error: '探测连接失败，请检查网络连接后重试', unknown: '平台结果无法确认，请稍后重试' };
          next.set(item.targetAccountId, { updatedAt: Date.now(), message: messages[item.outcome], tone: item.outcome === 'available' ? 'success' : 'warning' });
        }
      }
      return next;
    });
    const count = result.succeeded + result.failed;
    const previous = savedProgress.current.id === result.id ? savedProgress.current.count : 0;
    savedProgress.current = { id: result.id, count };
    if (count > previous) onResultsRef.current();
    if (result.queued + result.running + result.notSavedOrRetained > 0) {
      setBatchId(result.id);
      sessionStorage.setItem(storageKey, result.id);
    } else {
      setBatchId(null);
      sessionStorage.removeItem(storageKey);
      if (completedId.current !== result.id) {
        completedId.current = result.id;
        if (count <= previous) onResultsRef.current();
      }
    }
  }

  useEffect(() => {
    if (!requestKey || starting) return;
    let live = true;
    let reading = false;
    async function recover() {
      if (reading) return;
      reading = true;
      try {
        const result = await getPersonalProbesByRequest(requestKey!);
        if (live) { clearPending(); accept(result); setNotice(''); }
      } catch (error: unknown) {
        if (live) {
          const status = ownerProblem(error).status;
          setNotice(status === 401 ? '登录已过期' : status === 404 ? '正在确认探测是否已启动' : '探测任务读取失败');
          if (status === 401) window.clearInterval(timer);
        }
      } finally { reading = false; }
    }
    const timer = window.setInterval(() => void recover(), 2000);
    void recover();
    return () => { live = false; window.clearInterval(timer); };
  }, [requestKey, starting]);

  useEffect(() => {
    if (!batchId) return;
    let live = true;
    let reading = false;
    async function refresh() {
      if (reading) return;
      reading = true;
      try {
        const result = await getPersonalProbes(batchId!);
        if (live) {
          accept(result); setNotice('');
          if (result.queued + result.running + result.notSavedOrRetained === 0) window.clearInterval(timer);
        }
      } catch (error: unknown) {
        if (live) {
          const status = ownerProblem(error).status;
          setDisconnected(true); setNotice(status === 401 ? '登录已过期' : '探测进度读取失败，正在重试');
          if (status === 401 || status === 404) {
            window.clearInterval(timer);
            sessionStorage.removeItem(storageKey); setBatchId(null);
          }
        }
      } finally { reading = false; }
    }
    const timer = window.setInterval(() => void refresh(), 2000);
    void refresh();
    return () => { live = false; window.clearInterval(timer); };
  }, [batchId]);

  async function startScope(scope: Scope, ids: ReadonlySet<string> = new Set(), account?: Account) {
    if (startLock.current || busy) return;
    startLock.current = true;
    setStarting(true); setStartingIds(new Set(ids)); setNotice(''); setBatch(null); setBatchId(null);
    let submitted = false;
    try {
      if (account) {
        const access = await getTargetPersonalAccess(account.id);
        if (!hasUsablePersonalAccess(access)) {
          setFeedback(current => new Map(current).set(account.id, { updatedAt: Date.now(), message: access.status === 'verifying' ? 'AT 正在获取，请完成后再探测' : access.status === 'session_expired' ? 'AT 已到期，请先获取 AT 后探测' : '未获取 AT，请先获取 AT 后探测', tone: 'warning' }));
          return;
        }
      }
      const blocked = await accountNetworkPrerequisite();
      if (blocked) { setNotice(blocked.message); setFeedback(current => { const next = new Map(current); for (const id of ids) next.set(id, { ...blocked, updatedAt: Date.now() }); return next; }); return; }
      const preview = await previewPersonalProbes(scope);
      if (preview.count === 0) { setNotice('暂无可探测的账号'); return; }
      if (ids.size && (preview.scope !== 'selected' || preview.count !== ids.size)) throw new Error('account_scope_changed');
      const key = crypto.randomUUID();
      sessionStorage.setItem(pendingStorageKey, key);
      sessionStorage.setItem(pendingIdsStorageKey, JSON.stringify([...ids]));
      setRequestKey(key); submitted = true;
      const result = await createPersonalProbes(scope, preview.count, preview.scopeToken, key, concurrency);
      clearPending(); accept(result);
    } catch (error: unknown) {
      const status = ownerProblem(error).status;
      const rejected = !submitted || (!!status && status >= 400 && status < 500 && status !== 408 && status !== 429);
      if (rejected) clearPending();
      setNotice(!rejected ? '正在确认探测是否已启动' : status === 401 ? '登录已过期' : status === 403 ? '没有启动探测的权限' : status === 409 ? '账号资料已变化，请刷新后重试' : '探测启动失败，请重试');
    } finally { if (!submitted) clearPending(); startLock.current = false; setStarting(false); }
  }

  function start(ids: ReadonlySet<string>) {
    if (ids.size === 0 || ids.size > 10000) return;
    return startScope({ targetAccountIds: [...ids].sort() }, ids);
  }

  function startAll() { return startScope({}); }
  function startAccount(account: Account) { return startScope({ targetAccountIds: [account.id] }, new Set([account.id]), account); }

  function dismissBatch() { if (!busy) setBatch(null); }

  async function cancel() {
    if (!batch || canceling) return;
    setCanceling(true); setNotice('');
    try { accept(await cancelPersonalProbes(batch.id)); }
    catch { setNotice('取消结果待确认'); }
    finally { setCanceling(false); }
  }

  function stopTracking() {
    if (starting || !requestKey) return;
    clearPending(); setNotice('已停止追踪，探测是否启动尚未确认');
  }

  const processingIds = new Set(startingIds);
  if (batch) for (const item of batch.items) if (item.status === 'queued' || item.status === 'running') processingIds.add(item.targetAccountId);

  return { start, startAccount, startAll, feedback, dismissBatch, cancel, stopTracking, batch, busy, starting, canceling, processingIds, notice, dismissNotice, disconnected };
}
