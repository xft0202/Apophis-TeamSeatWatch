import { useActionFeedback } from '../shared/useActionFeedback';
import { useDebouncedValue, useDocumentVisibility } from '@mantine/hooks';
import { useEffect, useMemo, useRef, useState } from 'react';
import { ownerProblem } from './auth';
import { redemptionRecordsApi, type CredentialState, type ReclaimState, type RedemptionDetail, type RedemptionFocus, type RedemptionList, type RedemptionRecord } from './redemptionRecords';
import { dateBoundary } from '../shared/dateTime';
import { useMotherWorkspaceScope } from './useMotherWorkspaceScope';

type Filters = { scope: string; page: number; pageSize: number; search: string; sourceId: string | null; sourceName: string; from: string; to: string; credential?: CredentialState | undefined; reclaim?: ReclaimState | undefined };
const emptyFilters = (scope: string, pageSize = 20): Filters => ({ scope, page: 1, pageSize, search: '', sourceId: null, sourceName: '', from: '', to: '' });
function problem(error: unknown, fallback: string) {
  const value = ownerProblem(error);
  if (value.status === 401) return '登录已失效，请重新登录。';
  if (value.status === 404) return '此母号和空间下未找到该兑换记录。';
  if (value.status === 409) return '找回状态已变化，请刷新记录后重试。';
  return fallback;
}
export function useRedemptionRecords(active: boolean, focus: RedemptionFocus | null) {
  const [notice, setNotice, dismissNotice] = useActionFeedback('');
  const [readFailed, setReadFailed] = useState(false);
  const [feedback, setFeedback, dismissFeedback] = useActionFeedback('');
  const [requestNotice, setRequestNotice, dismissRequestNotice] = useActionFeedback('');
  const scope = useMotherWorkspaceScope(active, focus?.batchId ?? null, setNotice);
  const scopeKey = `${scope.motherId ?? ''}:${scope.workspaceId ?? ''}`;
  const ready = Boolean(scope.motherId && scope.workspaceId);
  const [savedFilters, setFilters] = useState<Filters>(emptyFilters(''));
  const filters = useMemo(() => savedFilters.scope === scopeKey ? savedFilters : emptyFilters(scopeKey, savedFilters.pageSize), [savedFilters, scopeKey]);
  const [debouncedSearch] = useDebouncedValue(filters.search, 300);
  const [records, setRecords] = useState<{ key: string; data: RedemptionList } | null>(null);
  const [loading, setLoading] = useState(false);
  const [revision, setRevision] = useState(0);
  const [selected, setSelected] = useState<{ scope: string; id: string } | null>(null);
  const selectedId = selected?.scope === scopeKey ? selected.id : null;
  const [detail, setDetail] = useState<RedemptionDetail | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [historyPage, setHistoryPage] = useState(1);
  const [historySize, setHistorySize] = useState(20);
  const [timelinePage, setTimelinePage] = useState(1);
  const [timelineSize, setTimelineSize] = useState(20);
  const [pending, setPending] = useState(false);
  const lock = useRef(false);
  const authorization = useRef<{ id: string; key: string; requestedAt: string } | null>(null);
  const detailRead = useRef<AbortController | null>(null);
  const visible = useDocumentVisibility() === 'visible';
  const key = JSON.stringify({ ...filters, membershipId: focus?.membershipId });
  const invalidDates = Boolean(filters.from && filters.to && filters.from > filters.to);
  const query = useMemo(() => {
    const from = dateBoundary(filters.from), before = dateBoundary(filters.to, true);
    return { mother_account_id: scope.motherId ?? '', workspace_id: scope.workspaceId ?? '', page: filters.page, page_size: filters.pageSize,
    ...(debouncedSearch.trim() ? { search: debouncedSearch.trim() } : {}), ...(filters.sourceId ? { source_batch_id: filters.sourceId } : {}),
    ...(focus?.membershipId ? { membership_id: focus.membershipId } : {}), ...(filters.credential ? { credential_state: filters.credential } : {}), ...(filters.reclaim ? { reclaim_state: filters.reclaim } : {}),
    ...(from ? { redeemed_from: from } : {}), ...(before ? { redeemed_before: before } : {}),
  }; }, [scope.motherId, scope.workspaceId, filters, debouncedSearch, focus?.membershipId]);
  useEffect(() => {
    if (!focus) return;
    setSelected({ scope: `${focus.motherAccountId}:${focus.workspaceId}`, id: focus.membershipId }); setDetail(null); setNotice(''); setRequestNotice(''); setFeedback(''); setHistoryPage(1); setTimelinePage(1);
  }, [focus]);
  useEffect(() => {
    if (!active || !ready || selectedId || invalidDates || filters.search !== debouncedSearch) { setLoading(false); return; }
    const controller = new AbortController(); setLoading(true); setReadFailed(false); setNotice('');
    void redemptionRecordsApi.list(query, controller.signal).then((value) => {
      if (controller.signal.aborted) return;
      const last = Math.max(1, Math.ceil(value.total / filters.pageSize));
      if (filters.page > last) { setFilters({ ...filters, page: last }); return; }
      setRecords({ key, data: value });
    }).catch((error: unknown) => { if (!controller.signal.aborted) { setRecords(null); setReadFailed(true); setNotice(problem(error, '兑换记录读取失败，请刷新重试。')); } }).finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [active, ready, selectedId, query, key, invalidDates, filters, debouncedSearch, revision]);
  useEffect(() => {
    setDetail(null);
    if (!active || !visible || !ready || !selectedId) { setDetailLoading(false); return; }
    const controller = new AbortController(); detailRead.current = controller; let timer: ReturnType<typeof setTimeout> | undefined;
    async function read() {
      setDetailLoading(true); setNotice('');
      try {
        const value = await redemptionRecordsApi.detail(selectedId!, { mother_account_id: scope.motherId!, workspace_id: scope.workspaceId!, reclaim_page: historyPage, reclaim_page_size: historySize as 20 | 50 | 100, timeline_page: timelinePage, timeline_page_size: timelineSize as 20 | 50 | 100 }, controller.signal);
        if (controller.signal.aborted) return;
        setDetail(value);
        const lastHistory = Math.max(1, Math.ceil(value.reclaims.total / historySize)), lastTimeline = Math.max(1, Math.ceil(value.timeline.total / timelineSize));
        if (historyPage > lastHistory) setHistoryPage(lastHistory); if (timelinePage > lastTimeline) setTimelinePage(lastTimeline);
        const attempt = authorization.current;
        if (attempt?.id === value.record.membershipId && value.record.reclaimRequestedAt !== attempt.requestedAt) { authorization.current = null; setRequestNotice(''); }
        if (value.record.reclaimState === 'queued' || value.record.reclaimState === 'running') timer = setTimeout(() => void read(), 3000);
      } catch (error: unknown) { if (!controller.signal.aborted) { setDetail(null); setNotice(problem(error, '兑换详情读取失败，请刷新重试。')); } }
      finally { if (!controller.signal.aborted) setDetailLoading(false); }
    }
    void read(); return () => { controller.abort(); if (timer) clearTimeout(timer); };
  }, [active, visible, ready, selectedId, scope.motherId, scope.workspaceId, historyPage, historySize, timelinePage, timelineSize, revision]);
  const [batchSearch, setBatchSearch] = useState('');
  const [batchDebounced] = useDebouncedValue(batchSearch, 300);
  const [batchPage, setBatchPage] = useState(1);
  const [batchLoading, setBatchLoading] = useState(false);
  const [batches, setBatches] = useState<{ key: string; items: Array<{ id: string; name: string }>; total: number } | null>(null);
  const batchKey = `${scopeKey}:${batchSearch}`;
  useEffect(() => { setBatchSearch(''); setBatchPage(1); }, [scopeKey]);
  useEffect(() => {
    if (!active || !ready || selectedId || batchSearch !== batchDebounced) return;
    const controller = new AbortController(); setBatchLoading(true);
    void redemptionRecordsApi.batches({ mother_account_id: scope.motherId!, workspace_id: scope.workspaceId! }, batchDebounced.trim(), batchPage, controller.signal).then((value) => {
      if (!controller.signal.aborted) setBatches((current) => ({ key: batchKey, items: batchPage === 1 || current?.key !== batchKey ? value.items : [...current.items, ...value.items], total: value.total }));
    }).catch(() => { if (!controller.signal.aborted) setNotice('来源批次读取失败，请刷新重试。'); }).finally(() => { if (!controller.signal.aborted) setBatchLoading(false); });
    return () => controller.abort();
  }, [active, ready, selectedId, scopeKey, scope.motherId, scope.workspaceId, batchSearch, batchDebounced, batchPage, batchKey, revision]);
  const batchOptions = new Map((batches?.key === batchKey ? batches.items : []).map((item) => [item.id, { value: item.id, label: item.name }]));
  if (filters.sourceId) batchOptions.set(filters.sourceId, { value: filters.sourceId, label: filters.sourceName });
  const data = ready && records?.key === key && !invalidDates ? records.data : null;
  const currentDetail = selectedId && detail?.record.membershipId === selectedId && detail.record.motherAccountId === scope.motherId && detail.record.workspaceId === scope.workspaceId ? detail : null;
  function update(patch: Partial<Filters>) { setFilters({ ...filters, page: 1, ...patch }); setFeedback(''); }
  function scopeChanged() { detailRead.current?.abort(); setSelected(null); setDetail(null); setFilters(emptyFilters('', filters.pageSize)); setNotice(''); setFeedback(''); setRequestNotice(''); }
  async function authorize() {
    if (lock.current || !currentDetail?.record.canAuthorizeReclaim || detailLoading) return;
    const record = currentDetail.record; lock.current = true; setPending(true); setNotice(''); setRequestNotice(''); setFeedback(''); detailRead.current?.abort();
    const attempt = authorization.current?.id === record.membershipId ? authorization.current : { id: record.membershipId, key: crypto.randomUUID(), requestedAt: record.reclaimRequestedAt ?? '' };
    authorization.current = attempt;
    try { await redemptionRecordsApi.authorize(record.membershipId, attempt.key); setFeedback('已提交重新授权。'); }
    catch (error: unknown) { setRequestNotice(problem(error, '请求结果待核验，请刷新查看；再次提交会使用原请求。')); }
    finally { lock.current = false; setPending(false); setRevision((value) => value + 1); }
  }
  return { scope, scopeChanged, ready, filters, data, loading: loading || scope.scopeLoading || (ready && filters.search !== debouncedSearch), readFailed, notice: requestNotice || notice, dismissNotice: () => { dismissNotice(); dismissRequestNotice(); }, feedback, dismissFeedback, invalidDates,
    selectedId, detail: currentDetail, detailLoading, pending, authorize, historyPage, historySize, timelinePage, timelineSize,
    update, resetFilters: () => { setFilters(emptyFilters(scopeKey, filters.pageSize)); setBatchSearch(''); setBatchPage(1); setFeedback(''); },
    refresh: () => { setRevision((value) => value + 1); scope.refresh(); },
    openDetail: (record: RedemptionRecord) => { setSelected({ scope: scopeKey, id: record.membershipId }); setDetail(null); setNotice(''); setRequestNotice(''); setFeedback(''); setHistoryPage(1); setTimelinePage(1); },
    closeDetail: () => { detailRead.current?.abort(); setSelected(null); setDetail(null); setNotice(''); setRequestNotice(''); setFeedback(''); setRevision((value) => value + 1); },
    setHistoryPage, setHistorySize: (value: number) => { setHistorySize(value); setHistoryPage(1); }, setTimelinePage, setTimelineSize: (value: number) => { setTimelineSize(value); setTimelinePage(1); },
    batchOptions: [...batchOptions.values()], batchSearch, batchLoading, moreBatches: () => setBatchPage((value) => value + 1), searchBatches: (value: string) => { setBatchSearch(value); setBatchPage(1); }, hasMoreBatches: batches?.key === batchKey && batchPage * 20 < batches.total,
  };
}
export type RedemptionRecordsState = ReturnType<typeof useRedemptionRecords>;
