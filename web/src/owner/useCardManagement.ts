import { useActionFeedback } from '../shared/useActionFeedback';
import { useDebouncedValue } from '@mantine/hooks';
import { useEffect, useMemo, useRef, useState } from 'react';
import { useRecordCopy } from '../shared/useRecordCopy';
import { formatCalendarDate } from '../shared/dateTime';
import { ownerProblem } from './auth';
import { cardManagementApi, type CardQuery, type CardSelectionEntry, type CardState } from './cardManagement';
import { getDeliveryRecord, revokeDeliveryCard, type DeliveryRecord, type DeliveryRecords } from './deliveryRecords';
import { useMotherWorkspaceScope } from './useMotherWorkspaceScope';

function problem(error: unknown, fallback: string) {
  const value = ownerProblem(error);
  if (value.status === 401) return '登录已失效，请重新登录。';
  if (value.code === 'card_secret_unavailable') return '此卡密未保存完整内容，只能查看原记录和尾号。';
  if (value.status === 409) return '卡密或选择范围已变化，请刷新后重新选择。';
  if (value.code === 'card_selection_limit') return '最多选择 10000 张卡密，请缩小筛选范围。';
  return fallback;
}
export function useCardManagement(active: boolean, batchId: string | null) {
  const [notice, setNotice, dismissNotice] = useActionFeedback('');
  const [readFailed, setReadFailed] = useState(false);
  const [feedback, setFeedback, dismissFeedback] = useActionFeedback('');
  const scope = useMotherWorkspaceScope(active, batchId, setNotice);
  const scopeKey = `${scope.motherId ?? ''}:${scope.workspaceId ?? ''}:${batchId ?? ''}`;
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [search, setSearch] = useState('');
  const [state, setState] = useState<CardState | undefined>();
  const [debouncedSearch] = useDebouncedValue(search, 300);
  const [records, setRecords] = useState<{ key: string; data: DeliveryRecords } | null>(null);
  const [loading, setLoading] = useState(false);
  const [selection, setSelection] = useState<{ scope: string; items: Map<string, CardSelectionEntry> }>({ scope: '', items: new Map() });
  const [selecting, setSelecting] = useState(false);
  const [selectedFilter, setSelectedFilter] = useState<string | null>(null);
  const selectionRead = useRef<AbortController | null>(null);
  const [detailId, setDetailId] = useState<string | null>(null);
  const [detail, setDetail] = useState<DeliveryRecord | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [confirmRevoke, setConfirmRevoke] = useState<DeliveryRecord | null>(null);
  const [pending, setPending] = useState<string | null>(null);
  const lock = useRef(false);
  const [revision, setRevision] = useState(0);
  const copier = useRecordCopy();
  const ready = Boolean(scope.motherId && scope.workspaceId);
  const filterKey = `${scopeKey}:${search.trim()}:${state ?? ''}`;
  const key = `${filterKey}:${page}:${pageSize}`;
  const keyRef = useRef(key); keyRef.current = key;
  const query: CardQuery = useMemo(() => ({
    ...(scope.motherId ? { mother_account_id: scope.motherId } : {}), ...(scope.workspaceId ? { workspace_id: scope.workspaceId } : {}),
    ...(batchId ? { batch_id: batchId } : {}), page, page_size: pageSize,
    ...(debouncedSearch.trim() ? { search: debouncedSearch.trim() } : {}), ...(state ? { card_state: state } : {}), cards_only: true,
  }), [scope.motherId, scope.workspaceId, batchId, page, pageSize, debouncedSearch, state]);
  const selected = selection.scope === scopeKey ? selection.items : new Map<string, CardSelectionEntry>();
  const data = records?.key === key && ready ? records.data : null;
  const allFilteredSelected = selectedFilter === filterKey || Boolean(data && data.total > 0 && data.items.length === data.total && data.items.every((item) => selected.has(item.membershipId)));
  const busy = pending !== null || copier.copyingId !== null;
  useEffect(() => { if (batchId) { setSearch(''); setState(undefined); } }, [batchId]);
  useEffect(() => {
    setPage(1); setDetailId(null); setDetail(null); setConfirmRevoke(null); setSelection({ scope: scopeKey, items: new Map() }); setFeedback('');
  }, [scopeKey]);
  useEffect(() => {
    selectionRead.current?.abort(); setSelecting(false);
    if (!active || !ready || detailId || search !== debouncedSearch) { setLoading(false); return; }
    const controller = new AbortController(); setLoading(true); setReadFailed(false); setNotice('');
    void cardManagementApi.list(query, controller.signal).then((value) => {
      if (controller.signal.aborted) return;
      const last = Math.max(1, Math.ceil(value.total / pageSize)); if (page > last) { setPage(last); return; }
      setRecords({ key, data: value });
    }).catch((error: unknown) => { if (!controller.signal.aborted) { setRecords(null); setReadFailed(true); setNotice(problem(error, '卡密读取失败，请刷新重试。')); } }).finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [active, scopeKey, ready, key, search, debouncedSearch, detailId, revision, query, page, pageSize]);
  useEffect(() => {
    if (!active || !detailId) return;
    const controller = new AbortController(); setDetail(null); setDetailLoading(true);
    void getDeliveryRecord(detailId, controller.signal).then((value) => { if (!controller.signal.aborted) setDetail(value); }).catch((error: unknown) => { if (!controller.signal.aborted) setNotice(problem(error, '卡密详情读取失败，请重试。')); }).finally(() => { if (!controller.signal.aborted) setDetailLoading(false); });
    return () => controller.abort();
  }, [active, detailId, revision]);
  useEffect(() => () => selectionRead.current?.abort(), []);
  function toggle(records: DeliveryRecord[], remove: boolean) {
    const items = new Map(selected);
    for (const record of records) {
      if (remove) items.delete(record.membershipId);
      else if (record.cardVersion) items.set(record.membershipId, { membershipId: record.membershipId, cardVersion: record.cardVersion });
    }
    if (items.size > 10000) { setNotice('最多选择 10000 张卡密，请缩小范围。'); return; }
    setSelectedFilter(null); setSelection({ scope: scopeKey, items });
  }
  async function selectAll() {
    if (busy || selecting || !ready || search !== debouncedSearch) return;
    selectionRead.current?.abort(); const controller = new AbortController(); selectionRead.current = controller;
    const startKey = key; setSelecting(true); setNotice('');
    try { const value = await cardManagementApi.select(query, controller.signal); if (!controller.signal.aborted && startKey === keyRef.current) { setSelection({ scope: scopeKey, items: new Map(value.items.map((item) => [item.membershipId, item])) }); setSelectedFilter(filterKey); } }
    catch (error: unknown) { if (!controller.signal.aborted) setNotice(problem(error, '全选读取失败，请重试。')); }
    finally { if (!controller.signal.aborted) setSelecting(false); }
  }
  async function download() {
    if (lock.current || !selected.size || !scope.motherId || !scope.workspaceId) return;
    lock.current = true; setPending('export'); setNotice(''); setFeedback('');
    try {
      const value = await cardManagementApi.export(scope.motherId, scope.workspaceId, [...selected.values()]);
      if (value.items.length) {
        const url = URL.createObjectURL(new Blob([value.items.map((item) => `${item.targetIdentifier}----${item.cardSecret}`).join('\n') + '\n'], { type: 'text/plain;charset=utf-8' }));
        const link = document.createElement('a'); link.href = url; link.download = `卡密-${formatCalendarDate(new Date().toISOString())}.txt`; link.click(); URL.revokeObjectURL(url);
      }
      const result = `已导出 ${value.items.length} 张卡密`;
      if (value.unavailable.length) setNotice(`${result}；${value.unavailable.length} 张未保存完整卡密，无法导出。原记录已保留。`);
      else setFeedback(result);
    } catch (error: unknown) { setNotice(problem(error, '导出失败，请重试。')); }
    finally { lock.current = false; setPending(null); }
  }
  async function revoke() {
    if (lock.current || !confirmRevoke) return;
    const record = confirmRevoke; lock.current = true; setPending(`revoke:${record.membershipId}`); setNotice('');
    try {
      await revokeDeliveryCard(record.membershipId); setConfirmRevoke(null); setRevision((value) => value + 1); setFeedback(`已撤销 ${record.targetIdentifier} 的卡密`); setSelectedFilter(null);
      setSelection((value) => { const items = new Map(value.items); items.delete(record.membershipId); return { ...value, items }; });
    } catch (error: unknown) { setNotice(problem(error, '撤销失败，请重试。')); }
    finally { lock.current = false; setPending(null); }
  }
  return { scope, ready, allFilteredSelected, page, pageSize, search, state, data, selected, loading: loading || scope.scopeLoading || (ready && search !== debouncedSearch), selecting, busy, pending, readFailed, notice: notice || copier.notice, dismissNotice: () => { dismissNotice(); copier.dismissNotice(); }, feedback, dismissFeedback, detailId, detail, detailLoading, confirmRevoke, setConfirmRevoke, copier, toggle, selectAll, download, revoke,
    copy: (record: DeliveryRecord) => copier.copy(record.membershipId, async () => (await cardManagementApi.secret(record)).cardSecret),
    openDetail: (record: DeliveryRecord) => { setDetail(record); setDetailId(record.membershipId); setNotice(''); }, closeDetail: () => { setDetailId(null); setDetail(null); },
    refresh: () => { setRevision((value) => value + 1); scope.refresh(); }, setPage,
    setPageSize: (value: number) => { setPageSize(value); setPage(1); }, setSearch: (value: string) => { setSearch(value); setPage(1); }, setState: (value: CardState | undefined) => { setState(value); setPage(1); },
    clearSelection: () => { setSelectedFilter(null); setSelection({ scope: scopeKey, items: new Map() }); }, resetFilters: () => { setSearch(''); setState(undefined); setPage(1); },
  };
}
export type CardManagementState = ReturnType<typeof useCardManagement>;
