import { useDebouncedValue } from '@mantine/hooks';
import { useEffect, useRef, useState } from 'react';
import type { components } from '../generated/owner';
import { getMotherAccount, getMotherDiscovery, getSelectedWorkspaceAccess, getSelectedWorkspaceVerification, listChildMaterials, listMotherAccounts, ownerProblem } from './auth';
import { operationDraftApi, type Draft, type DraftChange } from './operationDraft';
import { batchOperationsApi } from './batchOperations';
import { standbyApi, type Batch, type Selection } from './standbyBatches';
import { standbyWriteSelection } from './standbySelection';
import { selectedWorkspaceOperationState } from './workspaceVerification';
import { defaultPlannedAt } from './useBatchOperation';
import { formatDateTimeInput, parseDateTimeInput } from '../shared/dateTime';

type Mother = components['schemas']['MotherAccount'];
export function useOperationSelection(active: boolean, onNotice: (message: string) => void, onRestore: (batchId: string | null) => void) {
  const [draft, setDraft] = useState<Draft | null>(null);
  const currentDraft = useRef<Draft | null>(null);
  const [loading, setLoading] = useState(true);
  const [pending, setPending] = useState(false);
  const locked = useRef(false);
  const [revision, setRevision] = useState(0);
  const [mother, setMother] = useState<Mother | null>(null);
  const [mothers, setMothers] = useState<Mother[]>([]);
  const [motherSearch, setMotherSearch] = useState('');
  const [motherOptionPage, setMotherOptionPage] = useState(1);
  const [motherTotal, setMotherTotal] = useState(0);
  const [batchSearch, setBatchSearch] = useState('');
  const [batchOptionPage, setBatchOptionPage] = useState(1);
  const [batchTotal, setBatchTotal] = useState(0);
  const [batches, setBatches] = useState<Batch[]>([]);
  const [source, setSource] = useState<Batch | null>(null);
  const [discovery, setDiscovery] = useState<components['schemas']['MotherDiscovery'] | null>(null);
  const [workspaceReady, setWorkspaceReady] = useState(false);
  const [workspaceStatus, setWorkspaceStatus] = useState('');
  const [selection, setSelection] = useState<Selection | null>(null);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [rows, setRows] = useState<components['schemas']['TargetAccount'][]>([]);
  const [rowTotal, setRowTotal] = useState(0);
  const [rowsLoading, setRowsLoading] = useState(false);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [plannedAt, setPlannedAt] = useState(defaultPlannedAt);
  const [dirty, setDirty] = useState(false);
  const [debouncedMother] = useDebouncedValue(motherSearch, 300);
  const [debouncedBatch] = useDebouncedValue(batchSearch, 300);
  const notice = useRef(onNotice); notice.current = onNotice;
  const restore = useRef(onRestore); restore.current = onRestore;
  const scope = useRef(0);
  const selectionRead = useRef<AbortController | null>(null);

  function adopt(value: Draft) { currentDraft.current = value; setDraft(value); }
  function reload() { setRevision((value) => value + 1); }
  async function load(value: Draft, signal?: AbortSignal) {
    if (signal?.aborted) return;
    adopt(value); restore.current(value.executionBatchId ?? null);
    const values = await Promise.allSettled([
      value.motherAccountId ? getMotherAccount(value.motherAccountId, signal) : null,
      value.batchId ? standbyApi.get(value.batchId, signal) : null,
      value.motherAccountId ? getMotherDiscovery(value.motherAccountId, signal) : null,
      value.batchId ? standbyApi.preview('batch', [], '', value.batchId, undefined, signal) : null,
    ]);
    if (signal?.aborted) return;
    const unauthorized = values.find((item) => item.status === 'rejected' && ownerProblem(item.reason).status === 401);
    if (unauthorized?.status === 'rejected') throw unauthorized.reason;
    const [savedMother, savedSource, savedDiscovery, savedSelection] = values;
    setMother(savedMother?.status === 'fulfilled' ? savedMother.value : null);
    setSource(savedSource?.status === 'fulfilled' ? savedSource.value : null);
    setDiscovery(savedDiscovery?.status === 'fulfilled' ? savedDiscovery.value : null);
    setSelection(savedSelection?.status === 'fulfilled' ? savedSelection.value : null);
    setSelected(new Set(value.children.map((child) => child.accountId)));
    setPlannedAt(value.plannedAt ? formatDateTimeInput(value.plannedAt) : defaultPlannedAt()); setDirty(false);
    if (!value.executionBatchId && values.some((item) => item.status === 'rejected')) notice.current('部分选择无法读取，请重新选择对应对象。');
  }

  useEffect(() => {
    if (!active || locked.current) return;
    const controller = new AbortController(); setLoading(true);
    void operationDraftApi.get(controller.signal).catch(async (error: unknown) => {
      if (!controller.signal.aborted && ownerProblem(error).status === 404) return operationDraftApi.start();
      throw error;
    }).then((value) => load(value, controller.signal)).catch((error: unknown) => {
      if (!controller.signal.aborted) notice.current(ownerProblem(error).status === 401 ? '登录已失效，请重新登录。' : '本轮选择读取失败，请刷新重试。');
    }).finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => { scope.current++; controller.abort(); selectionRead.current?.abort(); };
  }, [active, revision]);

  useEffect(() => {
    if (!active || motherSearch !== debouncedMother) return;
    const controller = new AbortController();
    void listMotherAccounts(debouncedMother, motherOptionPage, 20, controller.signal).then((value) => {
      if (controller.signal.aborted) return;
      setMothers((existing) => motherOptionPage === 1 ? value.items : [...existing, ...value.items]); setMotherTotal(value.total);
    }).catch(() => { if (!controller.signal.aborted) notice.current('母号列表读取失败，请刷新重试。'); });
    return () => controller.abort();
  }, [active, debouncedMother, motherSearch, motherOptionPage, revision]);
  useEffect(() => {
    if (!active || batchSearch !== debouncedBatch) return;
    const controller = new AbortController();
    void standbyApi.list(batchOptionPage, 20, debouncedBatch, '', controller.signal).then((value) => {
      if (controller.signal.aborted) return;
      setBatches((existing) => batchOptionPage === 1 ? value.items : [...existing, ...value.items]); setBatchTotal(value.total);
    }).catch(() => { if (!controller.signal.aborted) notice.current('批次列表读取失败，请刷新重试。'); });
    return () => controller.abort();
  }, [active, debouncedBatch, batchSearch, batchOptionPage, revision]);
  useEffect(() => {
    if (!active || !draft?.motherAccountId || !draft.workspaceId) { setWorkspaceReady(false); setWorkspaceStatus(''); return; }
    const controller = new AbortController(); setWorkspaceReady(false); setWorkspaceStatus('核对中');
    void Promise.all([
      getSelectedWorkspaceAccess(draft.workspaceId, draft.motherAccountId, controller.signal),
      getSelectedWorkspaceVerification(draft.workspaceId, draft.motherAccountId, controller.signal),
    ]).then(([access, facts]) => {
      if (controller.signal.aborted) return;
      const state = selectedWorkspaceOperationState(facts, access, Date.now());
      setWorkspaceReady(state.ready); setWorkspaceStatus(state.status);
    }).catch(() => { if (!controller.signal.aborted) setWorkspaceStatus('空间核对失败，请重新核验'); });
    return () => controller.abort();
  }, [active, draft?.motherAccountId, draft?.workspaceId, revision]);
  useEffect(() => {
    if (!active || !source) { setRows([]); setRowTotal(0); setRowsLoading(false); return; }
    const controller = new AbortController(); setRowsLoading(true);
    void listChildMaterials(page, '', undefined, pageSize, '', controller.signal, undefined, { batchId: source.id }).then((value) => {
      if (controller.signal.aborted) return;
      const last = Math.max(1, Math.ceil(value.total / pageSize)); if (page > last) { setPage(last); return; }
      setRows(value.items); setRowTotal(value.total);
    }).catch(() => { if (!controller.signal.aborted) notice.current('批次账号读取失败，请刷新重试。'); }).finally(() => { if (!controller.signal.aborted) setRowsLoading(false); });
    return () => controller.abort();
  }, [active, source?.id, page, pageSize, revision]);

  async function change(body: Omit<DraftChange, 'expectedVersion'>) {
    const value = currentDraft.current ?? await operationDraftApi.start();
    const next = await operationDraftApi.change({ ...body, expectedVersion: value.version }); adopt(next); return next;
  }
  async function mutate(execute: () => Promise<unknown>) {
    if (locked.current) return false;
    locked.current = true; setPending(true); notice.current('');
    try { await execute(); return true; }
    catch (error: unknown) { notice.current(ownerProblem(error).code === 'rotation_not_completed' ? '上一轮尚未全部确认清退，请回轮转管理核验。' : ownerProblem(error).status === 409 ? '选择或资料已变化，请刷新后重新核对。' : '本轮选择未保存，请检查后重试。'); return false; }
    finally { locked.current = false; setPending(false); }
  }
  async function chooseMother(id: string) {
    if (id === draft?.motherAccountId) return;
    await mutate(async () => {
      selectionRead.current?.abort();
      const value = await change({ choice: 'mother', motherAccountId: id });
      // Clear dependent local state immediately after the server accepts the
      // scope change, before any optional metadata read can fail.
      setSource(null); setSelection(null); setSelected(new Set()); setRows([]); setRowTotal(0); setPage(1); setRowsLoading(false); setWorkspaceReady(false); setWorkspaceStatus(''); setDirty(true);
      const [nextMother, nextDiscovery] = await Promise.all([getMotherAccount(id), getMotherDiscovery(id)]);
      setMother(nextMother); setDiscovery(nextDiscovery);
      if (value.workspaceId || value.batchId) notice.current('母号已切换，请重新选择空间和批次。');
    });
  }
  async function chooseWorkspace(id: string) {
    if (id === draft?.workspaceId) return;
    await mutate(async () => {
      await change({ choice: 'workspace', workspaceId: id });
      // A workspace change also invalidates the selected batch and account rows
      // on the server. Keep the client at the same clean state.
      selectionRead.current?.abort(); setSource(null); setSelection(null); setSelected(new Set()); setRows([]); setRowTotal(0); setPage(1); setRowsLoading(false); setWorkspaceReady(false); setWorkspaceStatus(''); setDirty(true);
    });
  }
  async function chooseBatch(id: string) {
    if (id && id === draft?.excludedSourceBatchId) { notice.current('下一轮请选择其他来源批次。'); return; }
    selectionRead.current?.abort(); const controller = new AbortController(); selectionRead.current = controller;
    setSelection(null); setSelected(new Set()); setSource(null); setPage(1); setDirty(true); setRows([]);
    if (!id) { setRowsLoading(false); return; }
    setRowsLoading(true); notice.current('');
    try {
      const [batch, exact] = await Promise.all([standbyApi.get(id, controller.signal), standbyApi.preview('batch', [], '', id, undefined, controller.signal)]);
      if (controller.signal.aborted) return;
      setSource(batch); setSelection(exact); setSelected(new Set(exact.members.map((item) => item.accountId)));
    } catch (error: unknown) { if (!controller.signal.aborted) { setRowsLoading(false); notice.current(ownerProblem(error).status === 409 ? '批次已变化，请刷新后重新选择。' : '批次选择读取失败，请重试。'); } }
  }
  async function selectEntireBatch() {
    if (!source) return;
    selectionRead.current?.abort(); const controller = new AbortController(); selectionRead.current = controller;
    const [batch, exact] = await Promise.all([standbyApi.get(source.id, controller.signal), standbyApi.preview('batch', [], '', source.id, undefined, controller.signal)]);
    if (!controller.signal.aborted) { setSource(batch); setSelection(exact); setSelected(new Set(exact.members.map((item) => item.accountId))); setDirty(true); }
  }
  async function prepare() {
    if (locked.current || !draft?.workspaceId || !draft.motherAccountId || !source || !selection || !selected.size || !workspaceReady) return null;
    locked.current = true; setPending(true); notice.current('');
    try {
      const planned = parseDateTimeInput(plannedAt);
      if (!planned || planned.getTime() <= Date.now()) { notice.current('计划清退时间必须晚于当前时间。'); return null; }
      const children = standbyWriteSelection(selection).members.filter((item) => selected.has(item.accountId));
      if (children.length !== selected.size) { notice.current('批次已变化，请重新选择账号。'); return null; }
      const value = await change({ choice: 'children', batchId: source.id, batchVersion: source.version, children });
      const facts = await batchOperationsApi.workspace(value.workspaceId!);
      const binding = facts.binding && facts.binding.motherAccountId === value.motherAccountId ? facts.binding : await batchOperationsApi.bind(value.motherAccountId!, value.workspaceId!);
      const batch = await operationDraftApi.prepare({ bindingId: binding.id, expectedVersion: value.version, plannedAt: planned.toISOString() });
      adopt(await operationDraftApi.get()); setDirty(false); return batch;
    } catch (error: unknown) {
      const problem = ownerProblem(error);
      notice.current(['rotation_same_source_batch', 'invalid_rotation_successor'].includes(problem.code ?? '') ? '下一轮请选择其他来源批次，并核对上一轮清退结果。' : problem.code === 'child_material_incomplete' ? '所选账号存在异常资料，请修复或取消选择后继续。' : problem.status === 409 ? '空间、批次或账号状态已变化，请重新核对。' : '本轮保存结果待确认，请刷新原操作后继续。'); return null;
    } finally { locked.current = false; setPending(false); }
  }
  async function newOperation(context?: { motherAccountId: string; workspaceId: string; previousBatchId?: string }) {
    return mutate(async () => {
      selectionRead.current?.abort(); setRowsLoading(false); setRowTotal(0);
      let value = await operationDraftApi.reset(context?.previousBatchId); restore.current(null);
      adopt(value); setSource(null); setSelection(null); setSelected(new Set()); setRows([]); setPage(1); setDirty(false);
      if (context) {
        value = await change({ choice: 'mother', motherAccountId: context.motherAccountId });
        const [access, facts] = await Promise.all([getSelectedWorkspaceAccess(context.workspaceId, context.motherAccountId), getSelectedWorkspaceVerification(context.workspaceId, context.motherAccountId)]);
        if (selectedWorkspaceOperationState(facts, access, Date.now()).ready) value = await change({ choice: 'workspace', workspaceId: context.workspaceId });
      }
      await load(value); setLoading(false);
    });
  }
  return { draft, loading, pending, dirty, mother, mothers, discovery, source, batches, motherSearch, batchSearch, motherTotal, batchTotal, motherOptionPage, batchOptionPage, workspaceReady, workspaceStatus, selection, selected, rows, rowTotal, rowsLoading, page, pageSize, plannedAt, chooseMother, chooseWorkspace, chooseBatch, prepare, newOperation, reload, selectEntireBatch,
    setMotherSearch: (text: string) => { setMotherSearch(text); setMotherOptionPage(1); }, setBatchSearch: (text: string) => { setBatchSearch(text); setBatchOptionPage(1); },
    moreMothers: () => setMotherOptionPage((value) => value + 1), moreBatches: () => setBatchOptionPage((value) => value + 1), setPage,
    setPageSize: (size: number) => { setPageSize(size); setPage(1); }, setPlannedAt: (value: string) => { setPlannedAt(value); setDirty(true); },
    setSelected: (value: Set<string>) => { setSelected(value); setDirty(true); },
  };
}
export type OperationSelectionState = ReturnType<typeof useOperationSelection>;
