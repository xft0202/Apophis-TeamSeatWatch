import { useDebouncedValue } from '@mantine/hooks';
import { useEffect, useRef, useState } from 'react';
import type { components } from '../generated/owner';
import { getMotherAccount, getMotherDiscovery, listMotherAccounts } from './auth';
import { batchOperationsApi } from './batchOperations';

type Mother = components['schemas']['MotherAccount'];
type Workspace = components['schemas']['MotherVisibleWorkspace'];
export function useMotherWorkspaceScope(active: boolean, batchId: string | null, onNotice: (message: string) => void) {
  const [mother, setMother] = useState<Mother | null>(null);
  const [workspaceId, setWorkspaceId] = useState<string | null>(null);
  const [workspaces, setWorkspaces] = useState<Workspace[]>([]);
  const [mothers, setMothers] = useState<Mother[]>([]);
  const [search, setSearch] = useState('');
  const [optionPage, setOptionPage] = useState(1);
  const [total, setTotal] = useState(0);
  const [optionsLoading, setOptionsLoading] = useState(false);
  const [scopeLoading, setScopeLoading] = useState(false);
  const [revision, setRevision] = useState(0);
  const [debouncedSearch] = useDebouncedValue(search, 300);
  const read = useRef<AbortController | null>(null);
  const hydratedBatch = useRef<string | null>(null);
  const notice = useRef(onNotice); notice.current = onNotice;
  useEffect(() => {
    if (!active || search !== debouncedSearch) return;
    const controller = new AbortController(); setOptionsLoading(true);
    void listMotherAccounts(debouncedSearch.trim(), optionPage, 20, controller.signal).then((value) => {
      if (controller.signal.aborted) return;
      setMothers((current) => optionPage === 1 ? value.items : [...current, ...value.items]); setTotal(value.total);
    }).catch(() => { if (!controller.signal.aborted) notice.current('母号列表读取失败，请刷新重试。'); }).finally(() => { if (!controller.signal.aborted) setOptionsLoading(false); });
    return () => controller.abort();
  }, [active, debouncedSearch, search, optionPage, revision]);
  useEffect(() => {
    if (!batchId) { hydratedBatch.current = null; return; }
    if (!active || hydratedBatch.current === batchId) return;
    read.current?.abort(); const controller = new AbortController(); read.current = controller;
    setScopeLoading(true); setMother(null); setWorkspaceId(null); setWorkspaces([]);
    void batchOperationsApi.batch(batchId, 1, 20, controller.signal).then(async ({ batch }) => {
      if (!batch.motherAccountId) throw new Error('本轮缺少母号');
      const [account, discovery] = await Promise.all([getMotherAccount(batch.motherAccountId, controller.signal), getMotherDiscovery(batch.motherAccountId, controller.signal)]);
      if (controller.signal.aborted) return;
      hydratedBatch.current = batchId;
      setMother(account); setWorkspaceId(batch.workspaceId);
      setWorkspaces(discovery.workspaces.some((item) => item.id === batch.workspaceId) ? discovery.workspaces : [...discovery.workspaces, { id: batch.workspaceId, displayName: batch.workspaceName, accessStatus: 'unknown' }]);
    }).catch(() => { if (!controller.signal.aborted) notice.current('本轮母号和空间读取失败，请重新选择。'); }).finally(() => { if (!controller.signal.aborted) setScopeLoading(false); });
    return () => controller.abort();
  }, [active, batchId]);
  useEffect(() => { if (!active) read.current?.abort(); return () => read.current?.abort(); }, [active]);
  async function chooseMother(id: string | null) {
    if (id === mother?.id) return;
    const selected = mothers.find((item) => item.id === id) ?? (id === mother?.id ? mother : null);
    if (selected?.status === 'disabled') { notice.current('停用母号不能作为当前操作母号。'); return; }
    read.current?.abort(); const controller = new AbortController(); read.current = controller;
    setMother(null); setWorkspaceId(null); setWorkspaces([]); setScopeLoading(Boolean(id));
    if (!id) return;
    try {
      const [account, discovery] = await Promise.all([getMotherAccount(id, controller.signal), getMotherDiscovery(id, controller.signal)]);
      if (!controller.signal.aborted) { setMother(account); setWorkspaces(discovery.workspaces); }
    } catch { if (!controller.signal.aborted) notice.current('母号空间读取失败，请重新选择。'); }
    finally { if (!controller.signal.aborted) setScopeLoading(false); }
  }
  const options = new Map(mothers.map((item) => [item.id, { value: item.id, label: item.loginIdentifier || item.displayName, disabled: item.status === 'disabled' }]));
  if (mother) options.set(mother.id, { value: mother.id, label: mother.loginIdentifier || mother.displayName, disabled: mother.status === 'disabled' });
  return { motherId: mother?.id ?? null, workspaceId, motherOptions: [...options.values()], workspaceOptions: workspaces.map((item) => ({ value: item.id, label: item.displayName, disabled: item.accessStatus !== 'readable' })), search, optionsLoading, scopeLoading, hasMore: optionPage * 20 < total,
    chooseMother, chooseWorkspace: (id: string | null) => { if (id && workspaces.find((item) => item.id === id)?.accessStatus !== 'readable') { notice.current('此空间当前不可读取，请先在空间管理完成核验。'); return; } setWorkspaceId(id); }, searchMothers: (value: string) => { setSearch(value); setOptionPage(1); }, moreMothers: () => setOptionPage((value) => value + 1), refresh: () => setRevision((value) => value + 1),
  };
}
export type MotherWorkspaceScope = ReturnType<typeof useMotherWorkspaceScope>;
