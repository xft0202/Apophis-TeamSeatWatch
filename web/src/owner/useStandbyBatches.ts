import { useActionFeedback } from '../shared/useActionFeedback';
import { useDebouncedValue } from '@mantine/hooks';
import { useEffect, useRef, useState } from 'react';
import type { components } from '../generated/owner';
import { listChildMaterials, ownerProblem, type AccountProbeFilter } from './auth';
import { standbyApi, type Batch, type Selection } from './standbyBatches';
import { standbyRangeLimitNotice } from './standbySelection';
import { togglePage } from './childSelection';

type View = 'list' | 'new' | 'members' | 'add';
type Query = { search: string; domain: string; page: number; pageSize: number; probe?: AccountProbeFilter | undefined; membership?: 'assigned' | 'unassigned' | undefined };
type Confirmation = { kind: 'transfer' | 'remove'; selection: Selection; name: string; batch?: Batch | undefined };
const emptyQuery = (): Query => ({ search: '', domain: '', page: 1, pageSize: 20 });
const normalizeDomain = (value: string) => value.trim().toLowerCase().replace(/^@/, '');

export default function useStandbyBatches(active: boolean) {
  const [view, setView] = useState<View>('list');
  const [listQuery, setListQuery] = useState<Query>(emptyQuery);
  const [query, setQuery] = useState<Query>(emptyQuery);
  const [listSearch] = useDebouncedValue(listQuery.search, 300);
  const [listDomain] = useDebouncedValue(listQuery.domain, 300);
  const [search] = useDebouncedValue(query.search, 300);
  const [domain] = useDebouncedValue(query.domain, 300);
  const [batches, setBatches] = useState<Batch[]>([]);
  const [batchTotal, setBatchTotal] = useState(0);
  const [batch, setBatch] = useState<Batch>();
  const [name, setName] = useState('');
  const [nameError, setNameError] = useState('');
  const [accounts, setAccounts] = useState<components['schemas']['TargetAccount'][]>([]);
  const [total, setTotal] = useState(0);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [loading, setLoading] = useState(false);
  const [notice, setNotice, dismissNotice] = useActionFeedback('');
  const [readFailed, setReadFailed] = useState(false);
  const [feedback, setFeedback] = useActionFeedback<Record<string, string>>({});
  const [pending, setPending] = useState('');
  const [confirmation, setConfirmation] = useState<Confirmation | null>(null);
  const [deleting, setDeleting] = useState<Batch | null>(null);
  const [revision, setRevision] = useState(0);
  const locked = useRef(false);
  const generation = useRef(0);
  const memberQuery = useRef<Query>(emptyQuery());
  const stable = view === 'list' ? listQuery.search === listSearch && listQuery.domain === listDomain : query.search === search && query.domain === domain;
  const busy = !!pending;

  useEffect(() => {
    if (!active) { generation.current++; setConfirmation(null); setDeleting(null); }
  }, [active]);
  useEffect(() => {
    if (!active) return;
    if (!stable) { setLoading(true); return; }
    const controller = new AbortController(); setLoading(true); setReadFailed(false);
    if (view === 'list') {
      void standbyApi.list(listQuery.page, listQuery.pageSize, listSearch.trim(), normalizeDomain(listDomain), controller.signal).then((result) => {
        if (controller.signal.aborted) return;
        const last = Math.max(1, Math.ceil(result.total / listQuery.pageSize));
        if (listQuery.page > last) { setListQuery((current) => ({ ...current, page: last })); return; }
        setBatches(result.items); setBatchTotal(result.total); setLoading(false);
      }).catch((error: unknown) => { if (!controller.signal.aborted) { setReadFailed(true); setNotice(message(error)); setBatches([]); setBatchTotal(0); setLoading(false); } });
    } else {
      void Promise.all([listChildMaterials(query.page, search.trim(), query.probe, query.pageSize, normalizeDomain(domain), controller.signal, undefined, {
        ...(query.membership ? { membershipStatus: query.membership } : {}), ...(view === 'members' && batch ? { batchId: batch.id } : view === 'add' && batch ? { excludeBatchId: batch.id } : {}),
      }), view === 'members' && batch ? standbyApi.get(batch.id, controller.signal) : Promise.resolve(undefined)]).then(([result, currentBatch]) => {
        if (controller.signal.aborted) return;
        if (currentBatch) setBatch(currentBatch);
        const last = Math.max(1, Math.ceil(result.total / query.pageSize));
        if (query.page > last) { setQuery((current) => ({ ...current, page: last })); return; }
        setAccounts(result.items); setTotal(result.total); setLoading(false);
      }).catch((error: unknown) => { if (!controller.signal.aborted) { setReadFailed(true); setNotice(message(error)); setAccounts([]); setTotal(0); setLoading(false); } });
    }
    return () => controller.abort();
  }, [active, view, stable, listQuery.page, listQuery.pageSize, listSearch, listDomain, query.page, query.pageSize, query.probe, query.membership, search, domain, batch?.id, revision]);

  function message(error: unknown) {
    const problem = ownerProblem(error);
    return problem.status === 401 ? '登录已过期' : problem.code === 'range_limit_exceeded' ? standbyRangeLimitNotice(problem.actualCount, 'save') : problem.status === 404 ? '批次已删除或不存在，请刷新列表' : problem.status === 409 ? '批次或账号已变化，请刷新列表后重试' : '操作未完成，请重试';
  }
  function invalidate() { generation.current++; setConfirmation(null); setDeleting(null); }
  function changeQuery(patch: Partial<Query>) {
    if (locked.current) return;
    invalidate(); setNotice('');
    const filters = 'search' in patch || 'domain' in patch || 'probe' in patch || 'membership' in patch;
    if (view === 'list') setListQuery((current) => ({ ...current, ...patch, ...(filters ? { page: 1 } : {}) }));
    else { setQuery((current) => ({ ...current, ...patch, ...(filters ? { page: 1 } : {}) })); if (filters) setSelected(new Set()); }
  }
  function open(next: View, target?: Batch) {
    if (locked.current) return;
    invalidate(); setNotice(''); setSelected(new Set()); setAccounts([]); setTotal(0);
    if (next === 'list') { setBatch(undefined); }
    if (next === 'new') { setBatch(undefined); setName(''); setNameError(''); setQuery(emptyQuery()); }
    if (next === 'members') {
      if (target) { setBatch(target); setQuery(emptyQuery()); }
      else setQuery(memberQuery.current);
    }
    if (next === 'add') {
      memberQuery.current = query;
      setQuery({ ...emptyQuery(), domain: batch?.domains.length === 1 ? (batch.domains[0] ?? '') : '' });
    }
    setView(next);
  }
  function toggle(id?: string) {
    if (busy || loading || !stable) return;
    invalidate();
    setSelected((current) => {
      if (!id) return togglePage(current, accounts.map((item) => item.id));
      const next = new Set(current); if (next.has(id)) next.delete(id); else next.add(id); return next;
    });
  }
  async function task(key: string, work: () => Promise<void>) {
    if (locked.current) return;
    locked.current = true; setPending(key); setNotice('');
    try { await work(); }
    catch (error: unknown) { setNotice(message(error)); setConfirmation(null); }
    finally { locked.current = false; setPending(''); }
  }
  async function commit(change: Confirmation) {
    const saved = await standbyApi.save(change.name, change.selection, change.batch, change.kind === 'remove' ? 'remove' : 'add');
    setBatch(saved); setConfirmation(null); setSelected(new Set()); setRevision((current) => current + 1);
    if (view === 'new') { setView('list'); setNotice(`已保存批次 ${saved.name} · ${saved.memberCount} 个账号`); }
    else { if (view === 'add') { setQuery(memberQuery.current); setView('members'); } setNotice(change.kind === 'remove' ? `已移出 ${change.selection.count} 个账号` : `已添加 ${change.selection.count} 个账号`); }
  }
  async function save() {
    if (loading || !stable || selected.size === 0 || selected.size > 10000) return;
    if (view === 'new' && !name.trim()) { setNameError('请输入批次名称'); return; }
    await task('save', async () => {
      const current = generation.current;
      const selection = await standbyApi.preview('selected', [...selected], '');
      if (current !== generation.current) return;
      const change: Confirmation = { kind: 'transfer', selection, name: view === 'new' ? name.trim() : batch!.name, ...(view === 'add' ? { batch } : {}) };
      if (selection.members.some((member) => member.currentBatch && member.currentBatch.id !== change.batch?.id)) setConfirmation(change);
      else await commit(change);
    });
  }
  async function remove() {
    if (!batch || !selected.size || loading || !stable) return;
    await task('remove', async () => {
      const current = generation.current;
      const selection = await standbyApi.preview('selected', [...selected], '');
      if (current === generation.current) setConfirmation({ kind: 'remove', selection, name: batch.name, batch });
    });
  }
  async function confirm() { if (confirmation) await task('confirm', () => commit(confirmation)); }
  async function download(target: Batch) {
    await task(`export:${target.id}`, async () => {
      const selection = await standbyApi.preview('batch', [], '', target.id);
      if (selection.count === 0) { setFeedback((current) => ({ ...current, [target.id]: '批次没有账号' })); return; }
      const content = await standbyApi.export(target, selection);
      const url = URL.createObjectURL(new Blob([content], { type: 'text/plain;charset=utf-8' }));
      const link = document.createElement('a'); link.href = url; link.download = `${target.name.replace(/[\\/:*?"<>|]/g, '_')}.txt`; link.click(); URL.revokeObjectURL(url);
      setFeedback((current) => ({ ...current, [target.id]: `已导出 ${selection.count} 个账号` }));
    });
  }
  async function rename(target: Batch, nextName: string): Promise<boolean> {
    let saved = false;
    await task('rename', async () => {
      const result = await standbyApi.rename(target, nextName.trim());
      setBatches((current) => current.map((item) => item.id === result.id ? result : item));
      setFeedback((current) => ({ ...current, [target.id]: '已重命名' })); setRevision((current) => current + 1); saved = true;
    });
    return saved;
  }
  async function deleteBatch() {
    if (!deleting) return;
    const target = deleting;
    await task(`delete:${target.id}`, async () => {
      setFeedback((current) => ({ ...current, [target.id]: '正在删除批次' }));
      try {
        await standbyApi.delete(target);
      } catch (error: unknown) {
        setFeedback((current) => ({ ...current, [target.id]: message(error) }));
        throw error;
      } finally {
        setDeleting(null);
      }
      setBatches((current) => current.filter((item) => item.id !== target.id));
      setBatchTotal((current) => Math.max(0, current - 1));
      setNotice(`已删除批次 ${target.name} · ${target.memberCount} 个账号已解除归属`);
      setRevision((current) => current + 1);
    });
  }
  return { view, listQuery, query, batches, batchTotal, batch, name, nameError, setName: (value: string) => { setName(value); setNameError(''); }, accounts, total, selected, loading, readFailed, notice, dismissNotice, feedback, pending, busy, stable, confirmation, deleting, changeQuery, open, toggle, save, remove, confirm, download, rename, deleteBatch,
    requestDeletion: (target: Batch) => { if (!locked.current && !loading && stable) { invalidate(); setNotice(''); setDeleting(target); } },
    cancelDeletion: () => { if (!locked.current) setDeleting(null); },
    cancelConfirmation: () => { if (!locked.current) setConfirmation(null); },
    refresh: () => { if (!locked.current) { invalidate(); setNotice(''); setRevision((current) => current + 1); } },
  };
}
