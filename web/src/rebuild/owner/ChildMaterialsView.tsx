import {
  Alert, Badge, Button, Checkbox, FileInput, Group, Pagination, Paper,
  PasswordInput, Radio, ScrollArea, Stack, Table, Text, TextInput, Textarea,
} from '@mantine/core';
import { createColumnHelper, tableFeatures, useTable } from '@tanstack/react-table';
import { useEffect, useState } from 'react';
import type { components } from '../../generated/owner';
import { exportChildMaterials, importChildMaterials, listChildMaterials, ownerProblem, updateChildMaterial } from './auth';
import { exportRange, togglePage, type ChildScope } from './childSelection';

type Child = components['schemas']['TargetAccount'];
type ImportResult = components['schemas']['ChildMaterialsImportResult'];
const features = tableFeatures({});
const columnHelper = createColumnHelper<typeof features, Child>();
const columns = columnHelper.columns([
  columnHelper.accessor('id', { header: '' }),
  columnHelper.accessor('identifier', { header: '账号' }),
  columnHelper.accessor('materialStatus', { header: '资料状态' }),
]);

export default function ChildMaterialsView() {
  const [items, setItems] = useState<Child[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [search, setSearch] = useState('');
  const [text, setText] = useState('');
  const [file, setFile] = useState<File | null>(null);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [scope, setScope] = useState<ChildScope>('selected');
  const [confirmed, setConfirmed] = useState(false);
  const [importResult, setImportResult] = useState<ImportResult | null>(null);
  const [editing, setEditing] = useState<Child | null>(null);
  const [password, setPassword] = useState('');
  const [totp, setTotp] = useState('');
  const [notice, setNotice] = useState('');
  const [pending, setPending] = useState(false);
  const [loading, setLoading] = useState(true);
  const [revision, setRevision] = useState(0);
  const table = useTable({ data: items, columns, features });
  const range = exportRange(scope, selected, search, total);
  const pageIds = items.map((item) => item.id);
  const pageSelected = pageIds.length > 0 && pageIds.every((id) => selected.has(id));

  useEffect(() => {
    let active = true;
    setLoading(true);
    setItems([]);
    void listChildMaterials(page, search).then((result) => {
      if (active) { setItems(result.items); setTotal(result.total); setLoading(false); setConfirmed(false); }
    }).catch((error: unknown) => {
      if (active) { setNotice(ownerProblem(error).status === 401 ? '登录已失效。' : '子号资料无法加载。'); setTotal(0); setLoading(false); }
    });
    return () => { active = false; };
  }, [page, search, revision]);

  async function chooseFile(next: File | null) {
    setFile(next);
    if (next) setText(await next.text());
  }

  async function saveImport() {
    if (!text.trim()) { setNotice('请先选择或粘贴资料。'); return; }
    setPending(true); setNotice(''); setImportResult(null);
    try {
      const result = await importChildMaterials(text);
      setImportResult(result);
      setConfirmed(false);
      setSelected(new Set());
      setRevision((value) => value + 1);
    } catch (error: unknown) {
      setNotice(ownerProblem(error).status === 403 ? '没有保存资料的权限。' : '资料保存失败，请保留当前内容。');
    } finally { setPending(false); }
  }

  async function correct() {
    if (!editing || !password) return;
    setPending(true); setNotice('');
    try {
      await updateChildMaterial(editing, password, totp);
      setEditing(null); setPassword(''); setTotp(''); setConfirmed(false);
      setRevision((value) => value + 1);
    } catch (error: unknown) {
      setNotice(ownerProblem(error).status === 412 ? '资料已变化，请刷新后修正。' : '资料修正失败。');
    } finally { setPending(false); }
  }

  async function download() {
    if (loading || !confirmed || range.count < 1) return;
    setPending(true); setNotice('');
    try {
      const content = await exportChildMaterials(range.scope, range.accountIds, range.search, range.count);
      const url = URL.createObjectURL(new Blob([content], { type: 'text/plain;charset=utf-8' }));
      const link = document.createElement('a');
      link.href = url;
      link.download = 'child-account-materials.txt';
      link.click();
      URL.revokeObjectURL(url);
      setConfirmed(false);
    } catch (error: unknown) {
      setConfirmed(false);
      setNotice(ownerProblem(error).status === 409 ? '导出范围已变化，请重新确认数量。' : '资料导出失败。');
    } finally { setPending(false); }
  }

  return <Stack gap="xl">
    {notice ? <Alert color="error">{notice}</Alert> : null}
    <Paper withBorder radius={12} p="xl"><Stack gap="md">
      <Group justify="space-between"><Text fw={600}>导入子号资料</Text><FileInput value={file} onChange={chooseFile} accept="text/plain" placeholder="选择 TXT" clearable /></Group>
      <Textarea label="账号----密码----2FA" aria-label="子号资料文本" value={text} onChange={(event) => setText(event.currentTarget.value)} minRows={4} maxRows={10} autosize />
      <Group justify="flex-end"><Button onClick={() => void saveImport()} loading={pending}>保存资料</Button></Group>
      {importResult ? <Paper withBorder p="sm"><Text size="sm">已保存 {importResult.imported} · 重复 {importResult.duplicate} · 无效 {importResult.invalid}</Text>
        <Stack gap={4} mt="xs">{importResult.rows.filter((row) => row.status !== 'imported').map((row) => <Text key={row.line} size="xs">第 {row.line} 行 · {row.status === 'duplicate' ? '重复' : row.status === 'needs_totp' ? '资料待补' : '无效'}{row.identifier ? ` · ${row.identifier}` : ''}{row.message ? ` · ${row.message}` : ''}</Text>)}</Stack>
      </Paper> : null}
    </Stack></Paper>
    <Paper withBorder radius={12} p="xl"><Stack gap="md">
      <Group justify="space-between"><TextInput label="搜索邮箱或域名" value={search} onChange={(event) => { setSearch(event.currentTarget.value); setPage(1); setSelected(new Set()); setConfirmed(false); }} /><Button variant="light" onClick={() => { setConfirmed(false); setRevision((value) => value + 1); }}>刷新</Button></Group>
      <ScrollArea><Table striped highlightOnHover withTableBorder miw={640}><Table.Thead><Table.Tr>
        <Table.Th><Checkbox checked={pageSelected} disabled={loading} onChange={() => { setSelected(togglePage(selected, pageIds)); setConfirmed(false); }} aria-label="选择本页" /></Table.Th><Table.Th>账号</Table.Th><Table.Th>域名</Table.Th><Table.Th>资料状态</Table.Th><Table.Th>操作</Table.Th>
      </Table.Tr></Table.Thead><Table.Tbody>{table.getRowModel().rows.map((row) => <Table.Tr key={row.original.id}>
        <Table.Td><Checkbox checked={selected.has(row.original.id)} disabled={loading} onChange={() => { const next = new Set(selected); if (next.has(row.original.id)) next.delete(row.original.id); else next.add(row.original.id); setSelected(next); setConfirmed(false); }} aria-label={`选择 ${row.original.identifier}`} /></Table.Td>
        <Table.Td><Text ff="monospace" size="sm">{row.original.identifier}</Text></Table.Td>
        <Table.Td>{row.original.identifier.split('@')[1] ?? '—'}</Table.Td>
        <Table.Td><Badge color={row.original.materialStatus === 'complete' ? 'success' : 'warning'} variant="light">{row.original.materialStatus === 'complete' ? '资料已保存' : '资料待补'}</Badge></Table.Td>
        <Table.Td><Button variant="subtle" size="xs" onClick={() => { setEditing(row.original); setPassword(''); setTotp(''); }}>修正资料</Button></Table.Td>
      </Table.Tr>)}</Table.Tbody></Table></ScrollArea>
      <Group justify="space-between"><Text size="sm">共 {total} 条 · 已选 {selected.size} 条 · 第 {page} 页</Text><Pagination total={Math.max(1, Math.ceil(total / 20))} value={page} onChange={(value) => { setPage(value); setConfirmed(false); }} /></Group>
      {editing ? <Paper withBorder p="md"><Stack gap="sm"><Text fw={600}>修正 {editing.identifier}</Text><PasswordInput label="新密码" value={password} onChange={(event) => setPassword(event.currentTarget.value)} /><TextInput label="2FA" value={totp} onChange={(event) => setTotp(event.currentTarget.value)} /><Group justify="flex-end"><Button variant="default" onClick={() => setEditing(null)}>取消</Button><Button disabled={!password} loading={pending} onClick={() => void correct()}>保存修正</Button></Group></Stack></Paper> : null}
      <Radio.Group label="导出范围" value={scope} onChange={(value) => { setScope(value as ChildScope); setConfirmed(false); }}><Group mt="xs"><Radio value="selected" label={`已选跨页 ${selected.size} 条`} /><Radio value="filtered" label={`全部筛选结果 ${total} 条`} /></Group></Radio.Group>
      <Group justify="space-between"><Checkbox checked={confirmed} onChange={(event) => setConfirmed(event.currentTarget.checked)} disabled={loading || range.count < 1} label={`确认导出${scope === 'selected' ? '已选跨页' : '全部筛选结果'} ${range.count} 条`} /><Button onClick={() => void download()} loading={pending} disabled={loading || !confirmed || range.count < 1}>导出 TXT</Button></Group>
    </Stack></Paper>
  </Stack>;
}
