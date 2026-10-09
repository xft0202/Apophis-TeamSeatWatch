import ActionNotice from '../shared/ActionNotice';
import { useActionFeedback } from '../shared/useActionFeedback';
import {
  ActionIcon, Box, Button, Checkbox, FileInput, Group, Modal, Pagination, Paper,
  PasswordInput, ScrollArea, Select, Stack, Table, Text, TextInput, Textarea, Title,
} from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import { createColumnHelper, tableFeatures, useTable } from '@tanstack/react-table';
import { useEffect, useMemo, useRef, useState } from 'react';
import type { components } from '../generated/owner';
import { exportChildMaterials, importChildMaterials, listChildMaterials, ownerProblem, resolveFilteredChildMaterials, updateChildMaterial, type AccountProbeFilter, type AccountTokenFilter } from './auth';
import { togglePage } from './childSelection';
import OwnerIcon from './OwnerIcon';
import { usePersonalProbes } from './usePersonalProbes';
import { useAccountLogin } from './useAccountLogin';
import ListPagination from '../shared/ListPagination';
import StatusBadge from '../shared/StatusBadge';
import { useRecordCopy } from '../shared/useRecordCopy';
import { validTwoFactorSecret } from './materialValidation';
import { probeLabels, latestProbe } from './accountPresentation';
import { latestAccountFeedback } from './accountSession';
import AccountIdentity from './AccountIdentity';
import ListSelectionBar from '../shared/ListSelectionBar';
import useTaskConcurrency from './useTaskConcurrency';
import TaskConcurrencyControl from './TaskConcurrencyControl';
import Timestamp from '../shared/Timestamp';

type Child = components['schemas']['TargetAccount'];
type ImportResult = components['schemas']['ChildMaterialsImportResult'];
type AccountATFilter = Extract<AccountTokenFilter, 'has_at' | 'missing_at'>;
const tokenLabels: Record<AccountATFilter, string> = { has_at: '有 AT', missing_at: '无 AT' };

const features = tableFeatures({});
const columnHelper = createColumnHelper<typeof features, Child>();
const columns = columnHelper.columns([
  columnHelper.accessor('id', { header: '' }),
  columnHelper.accessor('identifier', { header: '账号' }),
]);

export default function ChildMaterialsView({ active: visible = true }: { active?: boolean }) {
  const [view, setView] = useState<'list' | 'import' | 'import-preview' | 'import-result'>('list');
  const [items, setItems] = useState<Child[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [search, setSearch] = useState('');
  const [domain, setDomain] = useState('');
  const [probeFilter, setProbeFilter] = useState<AccountProbeFilter | undefined>();
  const [tokenFilter, setTokenFilter] = useState<AccountATFilter | undefined>();
  const [debouncedSearch] = useDebouncedValue(search, 300);
  const [debouncedDomain] = useDebouncedValue(domain, 300);
  const selectedNames = useRef(new Map<string, string>());
  const [importPage, setImportPage] = useState(1);
  const [showImportIssues, setShowImportIssues] = useState(false);
  const [text, setText] = useState('');
  const [file, setFile] = useState<File | null>(null);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [importResult, setImportResult] = useState<ImportResult | null>(null);
  const [editing, setEditing] = useState<Child | null>(null);
  const [password, setPassword] = useState('');
  const [totp, setTotp] = useState('');
  const [notice, setNotice, dismissNotice] = useActionFeedback('');
  const [readFailed, setReadFailed] = useState(false);
  const [pending, setPending] = useState(false);
  const [loading, setLoading] = useState(true);
  const [revision, setRevision] = useState(0);
  const taskConcurrency = useTaskConcurrency(visible);
  const probes = usePersonalProbes(() => setRevision((value) => value + 1), taskConcurrency.concurrency);
  const accountLogin = useAccountLogin(() => setRevision((value) => value + 1), taskConcurrency.concurrency, taskConcurrency.limit);
  const copier = useRecordCopy();
  const table = useTable({ data: items, columns, features });
  const pageIds = items.map((item) => item.id);
  const pageSelected = pageIds.length > 0 && pageIds.every((id) => selected.has(id));
  const importPreview = useMemo(() => {
    const lines = text.split('\n');
    if (lines.at(-1) === '') lines.pop();
    const seen = new Set<string>();
    return lines.map((line, index) => {
      const fields = line.replace(/\r$/, '').split('----');
      const identifier = fields[0]?.trim().toLowerCase() ?? '';
      const email = /^[^\s<>@]+@[^\s<>@]+$/.test(identifier) && identifier.length <= 254;
      const format = fields.length === 3 && email && !!fields[1] && fields[1].length <= 1024 && (fields[2]?.length ?? 0) <= 1024;
      const completeTwoFactor = validTwoFactorSecret(fields[2] ?? '');
      const duplicate = format && seen.has(identifier);
      if (format) seen.add(identifier);
      return { line: index + 1, identifier: email && fields.length === 3 ? identifier : '—', format, duplicate, needsTwoFactor: !completeTwoFactor };
    });
  }, [text]);
  const importSummary = useMemo(() => {
    const invalid = importPreview.filter((row) => !row.format).length;
    const duplicate = importPreview.filter((row) => row.duplicate).length;
    const needsTwoFactor = importPreview.filter((row) => row.format && !row.duplicate && row.needsTwoFactor).length;
    const issues = importPreview.filter((row) => !row.format || (!row.duplicate && row.needsTwoFactor));
    return { invalid, duplicate, needsTwoFactor, complete: importPreview.length - invalid - duplicate - needsTwoFactor, issues };
  }, [importPreview]);
  const canImport = importPreview.length > importSummary.invalid;
  const importRows = showImportIssues ? importSummary.issues : importPreview;
  const importedNeedsTwoFactor = importResult?.rows.filter((row) => row.status === 'needs_totp').length ?? 0;

  useEffect(() => {
    if (!visible || view !== 'list') return;
    setLoading(true); setReadFailed(false); setNotice('');
    if (search !== debouncedSearch || domain !== debouncedDomain) return;
    const controller = new AbortController();
    void listChildMaterials(page, debouncedSearch.trim(), probeFilter, pageSize, debouncedDomain.trim().toLowerCase().replace(/^@/, ''), controller.signal, tokenFilter).then((result) => {
      if (controller.signal.aborted) return;
      const lastPage = Math.max(1, Math.ceil(result.total / pageSize));
      if (page > lastPage) { setPage(lastPage); return; }
      setItems(result.items); setTotal(result.total); setLoading(false);
    }).catch((error: unknown) => {
      if (!controller.signal.aborted) { setReadFailed(true); setNotice(ownerProblem(error).status === 401 ? '登录已过期' : '账号读取失败'); setItems([]); setTotal(0); setLoading(false); }
    });
    return () => controller.abort();
  }, [page, pageSize, search, debouncedSearch, domain, debouncedDomain, probeFilter, tokenFilter, revision, view, visible]);

  function clearSelection() { selectedNames.current.clear(); setSelected(new Set()); }

  function startImport(content = '') {
    setText(content); setFile(null); setImportPage(1); setShowImportIssues(false); setImportResult(null); setNotice(''); setView('import');
  }

  function updateSelection(next: Set<string>, accounts: Child[] = items) {
    const names = new Map(selectedNames.current);
    for (const account of accounts) if (next.has(account.id)) names.set(account.id, account.identifier);
    selectedNames.current = new Map([...names].filter(([id]) => next.has(id)));
    setSelected(next);
  }

  async function chooseFile(next: File | null) {
    setFile(next); setImportPage(1);
    if (!next) return;
    if (!next.name.toLowerCase().endsWith('.txt')) { setNotice('请选择 TXT 文件'); return; }
    try { setText(await next.text()); setNotice(''); }
    catch { setNotice('文件读取失败'); }
  }

  async function saveImport() {
    if (!text.trim()) { setNotice('请先选择或粘贴资料。'); return; }
    if (!canImport) { setNotice('没有可保存的账号，请返回修改。'); return; }
    setPending(true); setNotice(''); setImportResult(null);
    try {
      const result = await importChildMaterials(text);
      setImportResult(result);
      setView('import-result');

      clearSelection();
      setRevision((value) => value + 1);
    } catch (error: unknown) {
      setNotice(ownerProblem(error).status === 403 ? '没有保存权限' : '账号导入失败');
    } finally { setPending(false); }
  }

  async function correct() {
    if (!editing || (!password && !totp.trim())) return;
    setPending(true); setNotice('');
    try {
      await updateChildMaterial(editing, password, totp);
      if (totp.trim()) setImportResult((current) => current ? { ...current, rows: current.rows.map((row) => row.identifier === editing.identifier && row.status === 'needs_totp' ? { ...row, status: 'imported' } : row) } : current);
      setEditing(null); setPassword(''); setTotp('');
      setRevision((value) => value + 1);
    } catch (error: unknown) {
      setNotice(ownerProblem(error).status === 412 ? '资料已变化，请刷新后修正。' : '账号保存失败');
    } finally { setPending(false); }
  }

  function copyAccount(target: Child) {
    setNotice('');
    void copier.copy(target.id, async () => (await exportChildMaterials('selected', [target.id], '', 1)).replace(/\r?\n$/, ''));
  }

  async function repairTwoFactor(identifier: string) {
    setNotice('');
    try {
      const result = await listChildMaterials(1, identifier);
      const target = result.items.find((item) => item.identifier === identifier);
      if (!target) { setNotice('账号读取失败'); return; }
      setEditing(target); setPassword(''); setTotp('');
    } catch { setNotice('账号读取失败'); }
  }

  function startProbe(accountIds: ReadonlySet<string> = selected) {
    if (loading || pending || accountLogin.busy || accountIds.size === 0) return;
    const names = new Map([...selectedNames.current, ...items.map((account) => [account.id, account.identifier] as const)]);
    const identifiers = [...accountIds].map((id) => names.get(id)).filter((name): name is string => !!name);
    if (identifiers.length !== accountIds.size) { setNotice('已选账号已变化，请刷新后重新选择'); return; }
    setNotice(''); void probes.start(accountIds);
  }

  function startAllProbes() {
    if (loading || pending || accountLogin.busy || probes.busy) return;
    clearSelection(); setNotice(''); void probes.startAll();
  }

  function startAllAT() {
    if (loading || pending || probes.busy || accountLogin.busy) return;
    setNotice(''); void accountLogin.startAll();
  }

  function continueAllAT() {
    if (pending || probes.busy || accountLogin.busy || !accountLogin.batch) return;
    setNotice(''); void accountLogin.startAll(accountLogin.batch);
  }

  async function selectAllFiltered() {
    if (loading || pending || total > 10000) return;
    setPending(true); setNotice('');
    try {
      const accounts = await resolveFilteredChildMaterials(search.trim(), probeFilter, domain.trim().toLowerCase().replace(/^@/, ''), tokenFilter);
      const ids = new Set(accounts.map((account) => account.id));
      if (ids.size !== total || ids.size > 10000) throw new Error('account_filter_changed');
      updateSelection(ids, accounts);
    } catch { setNotice('账号范围已变化，请刷新后重新选择'); }
    finally { setPending(false); }
  }

  async function download() {
    if (loading || pending || selected.size === 0 || selected.size > 10000) return;
    setPending(true); setNotice('');
    try {
      const content = await exportChildMaterials('selected', [...selected].sort(), '', selected.size);
      const url = URL.createObjectURL(new Blob([content], { type: 'text/plain;charset=utf-8' }));
      const link = document.createElement('a');
      link.href = url;
      link.download = 'account-materials.txt';
      link.click();
      URL.revokeObjectURL(url);

    } catch (error: unknown) {

      setNotice(ownerProblem(error).status === 409 ? '已选账号已变化，请刷新后重新选择' : '资料导出失败。');
    } finally { setPending(false); }
  }

  const title = view === 'list' ? '账号管理' : '导入账号';

  return <Stack gap={24} className="accounts-page">
    <Group className="accounts-page-header" justify="space-between" align="center">
      <Group gap={12}>{view === 'list' ? <Box className="account-page-icon"><OwnerIcon name="accounts" size={22} /></Box> : null}<Title order={1}>{title}</Title></Group>
      {view === 'list' ? <Group gap={8} className="accounts-page-actions">
        <Button variant="default" leftSection={<OwnerIcon name="refresh" size={16} />} disabled={loading || pending || probes.busy || accountLogin.busy} title="探测所有账号，不受当前页和筛选限制" onClick={startAllProbes}>探测全部</Button>
        <Button variant="default" loading={accountLogin.batch?.status === 'running'} disabled={loading || pending || probes.busy || accountLogin.busy} title="获取所有账号的 AT，不受当前页和筛选限制" onClick={accountLogin.batch?.status === 'paused' || accountLogin.batch?.status === 'canceled' ? continueAllAT : startAllAT}>{accountLogin.batch?.status === 'paused' || accountLogin.batch?.status === 'canceled' ? '继续获取 AT' : accountLogin.batch?.status === 'completed' ? '重新获取全部 AT' : '获取全部 AT'}</Button>
        <Button variant="default" leftSection={<OwnerIcon name="download" size={16} />} loading={pending} disabled={loading || accountLogin.busy || selected.size === 0 || selected.size > 10000} onClick={() => void download()}>导出账号</Button>
        <Button leftSection={<OwnerIcon name="upload" size={16} />} disabled={accountLogin.busy || probes.busy} onClick={() => startImport()}>导入账号</Button>
      </Group> : <Button variant="default" leftSection={<OwnerIcon name="arrow-left" size={16} />} disabled={pending} onClick={() => setView('list')}>返回账号列表</Button>}
    </Group>
    <ActionNotice message={notice} onClose={dismissNotice} />
    <ActionNotice message={probes.notice} tone={probes.disconnected ? 'warning' : 'error'} onClose={probes.dismissNotice} />
    <ActionNotice message={accountLogin.notice} onClose={accountLogin.dismissNotice} />
    <ActionNotice message={copier.notice} onClose={copier.dismissNotice} />
    {view !== 'list' ? <Group gap={16} className="account-import-steps" aria-label="导入进度">
      {['录入资料', '核对账号', '导入结果'].map((label, index) => <Group key={label} gap={8} className={index === (view === 'import-result' ? 2 : view === 'import-preview' ? 1 : 0) ? 'account-import-step is-current' : 'account-import-step'}><Text className="account-step-number">{String(index + 1).padStart(2, '0')}</Text><Text size="sm">{label}</Text>{index < 2 ? <OwnerIcon name="arrow-right" size={12} /> : null}</Group>)}
    </Group> : null}
    {view === 'import' ? <Paper withBorder radius={12} className="account-import-panel"><Stack gap={24}>
      <Box className="account-import-source">
        <Box className="account-file-zone"><Box className="account-file-icon"><OwnerIcon name="upload" size={24} /></Box><Text fw={600}>TXT 文件</Text><FileInput value={file} onChange={chooseFile} accept=".txt,text/plain" aria-label="选择 TXT 文件" placeholder="选择 TXT 文件" clearable /></Box>
        <Textarea label="账号----密码----2FA" aria-label="账号资料文本" placeholder="email@example.com----password----2FA" value={text} onChange={(event) => { setText(event.currentTarget.value); setImportPage(1); }} minRows={8} maxRows={14} autosize className="account-import-text" />
      </Box>
      <Group justify="flex-end" className="account-panel-actions"><Button disabled={!text.trim()} rightSection={<OwnerIcon name="arrow-right" size={16} />} onClick={() => { setImportResult(null); setShowImportIssues(false); setImportPage(1); setView('import-preview'); }}>预览导入</Button></Group>
    </Stack></Paper> : null}
    {view === 'import-result' && importResult ? <Paper withBorder radius={12} p={{ base: 16, sm: 24 }}><Stack gap="md">
      <Text fw={600}>导入结果</Text>
      <Text size="sm" role="status">已保存 {importResult.imported} · 重复 {importResult.duplicate} · 无效 {importResult.invalid}{importedNeedsTwoFactor > 0 ? ` · 2FA 待补 ${importedNeedsTwoFactor}` : ''}</Text>
      <Stack gap={4} mt="xs">{importResult.rows.filter((row) => row.status !== 'imported').map((row) => <Group key={row.line} justify="space-between"><Text size="xs">第 {row.line} 行 · {row.status === 'duplicate' ? '重复' : row.status === 'needs_totp' ? '资料待补' : '无效'}{row.identifier ? ` · ${row.identifier}` : ''}</Text>{row.status === 'needs_totp' && row.identifier ? <Button size="xs" variant="subtle" onClick={() => void repairTwoFactor(row.identifier!)}>补齐2FA</Button> : null}</Group>)}</Stack>
      <Group justify="flex-end">
        {importResult.invalid > 0 ? <Button variant="default" onClick={() => { const lines = text.split('\n'); startImport(importResult.rows.filter((row) => row.status === 'invalid').map((row) => lines[row.line - 1] ?? '').join('\n')); }}>修正未导入</Button> : null}
        <Button onClick={() => startImport()}>继续导入</Button>
      </Group>
    </Stack></Paper> : null}
    {view === 'import-preview' ? <Paper withBorder radius={12} p={{ base: 16, sm: 24 }}><Stack gap="md">
      <Group justify="space-between"><Text fw={600}>导入预览 · {importPreview.length} 行</Text><Button variant="default" disabled={pending} onClick={() => setView('import')}>返回修改</Button></Group>
      <Group justify="space-between"><Text size="sm" role="status">完整 {importSummary.complete} · 2FA 待补 {importSummary.needsTwoFactor} · 文件内重复 {importSummary.duplicate} · 格式无效 {importSummary.invalid}</Text>{importSummary.issues.length > 0 ? <Button variant="subtle" size="xs" disabled={pending} onClick={() => { setShowImportIssues((value) => !value); setImportPage(1); }}>{showImportIssues ? '显示全部' : '查看待修正'}</Button> : null}</Group>
      {!canImport ? <Text size="sm" c="error" role="status">没有可保存的账号，请返回修改。</Text> : importSummary.issues.length > 0 ? <Text size="sm" c="warning" role="status">缺少或无效 2FA 的账号将保存为待补资料；格式无效的行会跳过，其他账号正常导入。</Text> : null}
      <Table.ScrollContainer minWidth={480}><Table><Table.Thead><Table.Tr><Table.Th>行号</Table.Th><Table.Th className="account-identity-cell">账号</Table.Th><Table.Th>格式</Table.Th></Table.Tr></Table.Thead>
        <Table.Tbody>{importRows.slice((importPage - 1) * 20, importPage * 20).map((row) => <Table.Tr key={row.line}><Table.Td>{row.line}</Table.Td><Table.Td className="account-identity-cell">{row.identifier}</Table.Td><Table.Td><StatusBadge tone={!row.format ? 'error' : row.needsTwoFactor || row.duplicate ? 'warning' : 'gray'} label={!row.format ? '格式待修正' : row.duplicate ? '文件内重复' : row.needsTwoFactor ? '2FA 待补' : '待导入'} /></Table.Td></Table.Tr>)}</Table.Tbody>
      </Table></Table.ScrollContainer>
      <Pagination value={importPage} total={Math.max(1, Math.ceil(importRows.length / 20))} onChange={setImportPage} disabled={pending} />
      <Group justify="flex-end"><Button loading={pending} disabled={!canImport} onClick={() => void saveImport()}>确认导入</Button></Group>
    </Stack></Paper> : null}
    {view === 'list' ? <Paper withBorder radius={12} className="account-list-panel">
      <Group px="md" pt="sm" justify="flex-end"><TaskConcurrencyControl value={taskConcurrency.concurrency} limit={taskConcurrency.limit} disabled={!taskConcurrency.ready || probes.busy || accountLogin.busy} onChange={taskConcurrency.setConcurrency} /></Group>
      <Box className="account-filters"><TextInput disabled={pending} aria-label="搜索账号" placeholder="搜索账号" leftSection={<OwnerIcon name="search" size={16} />} value={search} onChange={(event) => { setSearch(event.currentTarget.value); setPage(1); clearSelection(); }} />
        <TextInput disabled={pending} aria-label="邮箱域名" placeholder="邮箱域名" value={domain} onChange={(event) => { setDomain(event.currentTarget.value); setPage(1); clearSelection(); }} />
        <Select disabled={pending} aria-label="探测结果" clearable placeholder="全部探测结果" value={probeFilter ?? null} data={Object.entries(probeLabels).map(([value, label]) => ({ value, label }))} onChange={(value) => { setProbeFilter(value ? value as AccountProbeFilter : undefined); setPage(1); clearSelection(); }} />
        <Select disabled={pending} aria-label="AT 状态" clearable placeholder="全部 AT 状态" value={tokenFilter ?? null} data={Object.entries(tokenLabels).map(([value, label]) => ({ value, label }))} onChange={(value) => { setTokenFilter(value ? value as AccountATFilter : undefined); setPage(1); clearSelection(); }} />
        <Group className="account-filter-actions" gap={4} wrap="nowrap"><Button variant="subtle" color="gray" size="sm" disabled={pending || (!search && !domain && !probeFilter && !tokenFilter)} onClick={() => { setSearch(''); setDomain(''); setProbeFilter(undefined); setTokenFilter(undefined); setPage(1); clearSelection(); }}>重置</Button><ActionIcon variant="subtle" color="gray" size={32} aria-label="刷新" title="刷新" disabled={loading || pending} onClick={() => setRevision((value) => value + 1)}><OwnerIcon name="refresh" size={16} /></ActionIcon></Group>
      </Box>
      <ListSelectionBar count={selected.size} total={total} disabled={loading || pending} selecting={pending} onSelectAll={() => void selectAllFiltered()} onClear={clearSelection} actions={<Button variant="default" size="sm" disabled={loading || pending || probes.busy || accountLogin.busy} leftSection={<OwnerIcon name="refresh" size={16} />} onClick={() => startProbe()}>批量探测选中 {selected.size} 个</Button>} />
      {accountLogin.batch ? <Group className="account-task-bar account-at-task-bar" justify="space-between" role="status"><Group gap={8}><OwnerIcon name="refresh" size={16} /><Text size="sm">{accountLogin.batch.status === 'running' ? `正在获取 AT · 已处理 ${accountLogin.batch.processed} / ${accountLogin.batch.total ?? '…'}` : accountLogin.batch.status === 'completed' ? `获取 AT 完成 · 成功 ${accountLogin.batch.succeeded} · 失败 ${accountLogin.batch.failed} · 跳过 ${accountLogin.batch.skipped}` : `${accountLogin.batch.status === 'canceled' ? '已停止获取 AT' : '获取 AT 已暂停'} · 已处理 ${accountLogin.batch.processed} / ${accountLogin.batch.total ?? '…'}`}</Text>{accountLogin.batch.error ? <Text size="xs" c="warning">{accountLogin.batch.error}</Text> : null}</Group><Group gap={4}>{accountLogin.batch.status === 'running' ? <Button variant="subtle" color="error" size="xs" loading={accountLogin.stopping} onClick={accountLogin.stopAll}>停止获取</Button> : accountLogin.batch.status !== 'completed' ? <Button variant="subtle" size="xs" disabled={accountLogin.busy || probes.busy} onClick={continueAllAT}>继续获取</Button> : null}{accountLogin.batch.status !== 'running' ? <Button variant="subtle" color="gray" size="xs" onClick={accountLogin.dismissBatch}>清除记录</Button> : null}</Group></Group> : null}
      {probes.busy ? <Group className="account-task-bar" justify="space-between" role="status"><Group gap={8}><OwnerIcon name="refresh" size={16} /><Text size="sm">{probes.batch ? `正在探测${probes.batch.scope === 'filtered' ? '全部账号' : '选中账号'} · 已完成 ${probes.batch.succeeded + probes.batch.failed + probes.batch.canceled} / ${probes.batch.total}` : probes.starting ? '正在启动探测' : '正在恢复探测进度'}</Text>{probes.batch?.notSavedOrRetained ? <Text size="xs" c="warning">{probes.batch.notSavedOrRetained} 个结果待核实</Text> : null}</Group>{probes.batch && probes.batch.queued + probes.batch.running > 0 ? <Button variant="subtle" color="error" size="xs" loading={probes.canceling} onClick={() => void probes.cancel()}>取消待处理</Button> : !probes.starting && !probes.batch ? <Button variant="subtle" color="gray" size="xs" onClick={probes.stopTracking}>停止追踪</Button> : null}</Group> : null}
      <ScrollArea.Autosize mah="max(320px, calc(100dvh - 388px))" type="auto" className="account-table-scroll"><Table aria-busy={loading} highlightOnHover stickyHeader miw={1040} className="account-table"><Table.Thead><Table.Tr>
        <Table.Th><Checkbox checked={pageSelected} indeterminate={!pageSelected && pageIds.some((id) => selected.has(id))} disabled={loading || pending || items.length === 0} onChange={() => { updateSelection(togglePage(selected, pageIds)); }} aria-label="选择本页" /></Table.Th><Table.Th className="account-identity-cell">账号</Table.Th><Table.Th>域名</Table.Th><Table.Th>AT</Table.Th><Table.Th>最近探测</Table.Th><Table.Th>探测时间</Table.Th><Table.Th>操作</Table.Th>
      </Table.Tr></Table.Thead><Table.Tbody>{table.getRowModel().rows.map((row) => { const probe = latestProbe(row.original); const loggingIn = accountLogin.pendingIds.has(row.original.id); const probing = probes.processingIds.has(row.original.id); const tokens = row.original.tokenStatus; const feedback = probing ? probes.feedback.get(row.original.id) : latestAccountFeedback(accountLogin.feedback.get(row.original.id), probes.feedback.get(row.original.id)); return <Table.Tr key={row.original.id} data-selected={selected.has(row.original.id) || undefined}>
        <Table.Td><Checkbox checked={selected.has(row.original.id)} disabled={loading || pending} onChange={() => { const next = new Set(selected); if (next.has(row.original.id)) next.delete(row.original.id); else next.add(row.original.id); updateSelection(next); }} aria-label={`选择 ${row.original.identifier}`} /></Table.Td>
        <Table.Td className="account-identity-cell"><AccountIdentity identifier={row.original.identifier} /></Table.Td>
        <Table.Td><Text size="xs" className="account-domain">{row.original.identifier.split('@')[1] ?? '—'}</Text></Table.Td>
        <Table.Td><StatusBadge label={!tokens ? '—' : tokens.hasAccessToken ? '有' : '无'} tone={tokens?.hasAccessToken ? 'success' : 'gray'} /></Table.Td>
        <Table.Td><StatusBadge tone={probing ? 'indigo' : probe.status === 'available' ? 'success' : probe.status === 'unprobed' ? 'gray' : probe.status === 'definitely_unavailable' || probe.status === 'account_problem' ? 'error' : 'warning'} label={probing ? '探测中' : probe.label} /></Table.Td>
        <Table.Td><Timestamp value={probe.at} /></Table.Td>
        <Table.Td><Stack gap={4} align="center"><Group gap={4} wrap="nowrap" justify="center"><Button variant="subtle" size="xs" loading={copier.copyingId === row.original.id} disabled={loading || pending || copier.copyingId !== null} onClick={() => copyAccount(row.original)}>{copier.copiedId === row.original.id ? '已复制' : '复制'}</Button><Button variant="subtle" size="xs" loading={loggingIn} disabled={loading || pending || probing || row.original.status !== 'active'} onClick={() => { setNotice(''); void accountLogin.login(row.original); }}>{loggingIn ? '获取中' : '获取 AT'}</Button><Button variant="subtle" size="xs" loading={probing} disabled={loading || pending || probes.busy || loggingIn || row.original.status !== 'active'} onClick={() => { setNotice(''); void probes.startAccount(row.original); }}>探测</Button></Group>{feedback ? <Text size="xs" c={feedback.tone} role="status" maw={260}>{feedback.message}{feedback.proxy ? <> · <a href="#proxy">代理管理</a></> : null}</Text> : null}</Stack></Table.Td>
      </Table.Tr>; })}</Table.Tbody></Table></ScrollArea.Autosize>
      {loading ? <Text className="account-empty-state" role="status">正在读取账号</Text> : !notice && !items.length ? <Box className="account-empty-state"><OwnerIcon name={search || domain || probeFilter || tokenFilter ? 'search' : 'accounts'} size={24} /><Text c="dimmed" ta="center">{readFailed ? '账号读取失败，请刷新重试' : search || domain || probeFilter || tokenFilter ? '无匹配账号' : '暂无账号'}</Text></Box> : null}
      <ListPagination page={page} pageSize={pageSize} total={total} disabled={pending || loading} onPageChange={setPage} onPageSizeChange={(next) => { setPageSize(next); setPage(1); }} />
    </Paper> : null}
    <Modal opened={editing !== null} onClose={() => setEditing(null)} title={editing ? `编辑 ${editing.identifier}` : '编辑账号'} centered>
      <Stack gap="md"><PasswordInput label="新密码" value={password} onChange={(event) => setPassword(event.currentTarget.value)} /><TextInput label="新2FA" value={totp} onChange={(event) => setTotp(event.currentTarget.value)} /><Group justify="flex-end"><Button variant="default" onClick={() => setEditing(null)}>取消</Button><Button disabled={!password && !totp.trim()} loading={pending} onClick={() => void correct()}>保存</Button></Group></Stack>
    </Modal>
  </Stack>;
}
