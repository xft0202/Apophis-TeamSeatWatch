import {
  Alert,
  Badge,
  Button,
  Checkbox,
  FileInput,
  Group,
  Paper,
  PasswordInput,
  ScrollArea,
  Stack,
  Table,
  Text,
  TextInput,
  Textarea,
  Title,
} from '@mantine/core';
import { useEffect, useState } from 'react';
import { createColumnHelper, tableFeatures, useTable } from '@tanstack/react-table';
import type { components } from '../generated/owner';
import {
  exportMotherAccounts,
  importMotherAccounts,
  listMotherAccounts,
  updateMotherAccountMaterial,
  ownerProblem,
} from './auth';

type MotherAccount = components['schemas']['MotherAccount'];
type ImportResult = components['schemas']['MotherAccountImportResult'];
type ImportRow = components['schemas']['MotherAccountImportRow'];

const features = tableFeatures({});
const columnHelper = createColumnHelper<typeof features, MotherAccount>();
const columns = columnHelper.columns([
  columnHelper.accessor('id', { header: '' }),
  columnHelper.accessor('loginIdentifier', { header: '账号' }),
  columnHelper.accessor('materialStatus', { header: '资料状态' }),
  columnHelper.accessor('status', { header: '记录状态' }),
]);

export default function MotherMaterialsView() {
  const [items, setItems] = useState<MotherAccount[]>([]);
  const [total, setTotal] = useState(0);
  const [search, setSearch] = useState('');
  const [text, setText] = useState('');
  const [file, setFile] = useState<File | null>(null);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [importResult, setImportResult] = useState<ImportResult | null>(null);
  const [notice, setNotice] = useState('');
  const [pending, setPending] = useState<'load' | 'import' | 'export' | null>(null);
  const [confirmExport, setConfirmExport] = useState(false);
  const [editing, setEditing] = useState<MotherAccount | null>(null);
  const [editPassword, setEditPassword] = useState('');
  const [editTotp, setEditTotp] = useState('');

  const table = useTable({ data: items, columns, features });
  const selectedCount = selected.size;
  const exportCount = selectedCount > 0 ? selectedCount : total;

  async function load() {
    setPending('load');
    try {
      const result = await listMotherAccounts(search);
      setItems(result.items);
      setTotal(result.total);
      setSelected((current) => new Set([...current].filter((id) => result.items.some((item) => item.id === id))));
    } catch (error: unknown) {
      setNotice(ownerProblem(error).status === 401 ? '登录已失效。' : '资料暂时无法加载。');
    } finally {
      setPending(null);
    }
  }

  useEffect(() => { void load(); }, [search]);

  async function chooseFile(next: File | null) {
    setFile(next);
    if (!next) return;
    if (!next.name.toLowerCase().endsWith('.txt')) { setNotice('请选择 TXT 文件'); return; }
    try { setText(await next.text()); setNotice(''); }
    catch { setNotice('文件读取失败，已保留当前资料'); }
  }

  async function importText() {
    if (!text.trim()) {
      setNotice('请先选择或粘贴资料。');
      return;
    }
    setPending('import');
    setNotice('');
    try {
      const result = await importMotherAccounts(text);
      setImportResult(result);
      await load();
    } catch (error: unknown) {
      setNotice(ownerProblem(error).status === 403 ? '没有保存资料的权限。' : '资料保存失败，请保留当前内容后重试。');
    } finally {
      setPending(null);
    }
  }

  async function saveCorrection() {
    if (!editing || !editPassword) return;
    setPending('import');
    try {
      await updateMotherAccountMaterial(editing, editPassword, editTotp);
      setEditing(null);
      setEditPassword('');
      setEditTotp('');
      await load();
    } catch (error: unknown) {
      setNotice(ownerProblem(error).status === 403 ? '没有修正资料的权限。' : '资料修正失败。');
    } finally {
      setPending(null);
    }
  }

  async function exportText() {
    if (!confirmExport || exportCount < 1) return;
    setPending('export');
    setNotice('');
    try {
      const content = await exportMotherAccounts(selectedCount > 0 ? [...selected] : undefined, exportCount);
      const url = URL.createObjectURL(new Blob([content], { type: 'text/plain;charset=utf-8' }));
      const link = document.createElement('a');
      link.href = url;
      link.download = 'mother-account-materials.txt';
      link.click();
      URL.revokeObjectURL(url);
    } catch (error: unknown) {
      setNotice(ownerProblem(error).status === 409 ? '资料范围已变化，请重新确认数量。' : '资料导出失败。');
    } finally {
      setPending(null);
    }
  }

  const allVisibleSelected = items.length > 0 && items.every((item) => selected.has(item.id));
  const toggleAll = () => setSelected((current) => {
    const next = new Set(current);
    if (allVisibleSelected) items.forEach((item) => next.delete(item.id));
    else items.forEach((item) => next.add(item.id));
    return next;
  });
  const toggleOne = (id: string) => setSelected((current) => {
    const next = new Set(current);
    if (next.has(id)) next.delete(id); else next.add(id);
    return next;
  });

  return (
    <Stack gap="xl">
      <div>
        <Text size="xs" tt="uppercase" fw={600} c="dimmed" lts="0.08em">账号管理 / 母号资料</Text>
        <Title order={1} size="h2" mt="md">母号资料</Title>
      </div>
      {notice ? <Alert color="error">{notice}</Alert> : null}

      <Paper withBorder radius={12} p={{ base: 'lg', sm: 'xl' }}>
        <Stack gap="md">
          <Group justify="space-between" align="flex-end">
            <div>
              <Text fw={600}>导入三字段 TXT</Text>
              <Text size="sm" c="dimmed" mt={4}>账号----密码----2FA</Text>
            </div>
            <FileInput value={file} onChange={chooseFile} accept=".txt,text/plain" aria-label="选择 TXT 文件" placeholder="选择 TXT" clearable />
          </Group>
          <Textarea value={text} onChange={(event) => setText(event.currentTarget.value)} minRows={5} autosize maxRows={10} placeholder="每行一条资料" aria-label="母号资料文本" />
          <Group justify="space-between">

            <Button onClick={importText} loading={pending === 'import'}>保存资料</Button>
          </Group>
          {importResult ? <ImportFeedback result={importResult} /> : null}
        </Stack>
      </Paper>

      <Paper withBorder radius={12} p={{ base: 'lg', sm: 'xl' }}>
        <Stack gap="md">
          <Group justify="space-between" align="flex-end">
            <TextInput label="搜索账号" placeholder="输入账号或名称" value={search} onChange={(event) => setSearch(event.currentTarget.value)} />
            <Button variant="light" onClick={() => void load()} loading={pending === 'load'}>刷新</Button>
          </Group>
          <ScrollArea type="auto">
            <Table striped highlightOnHover withTableBorder miw={720} verticalSpacing="sm">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th><Checkbox checked={allVisibleSelected} onChange={toggleAll} aria-label="选择当前列表" /></Table.Th>
                  <Table.Th>账号</Table.Th>
                  <Table.Th>资料状态</Table.Th>
                  <Table.Th>记录状态</Table.Th>
                  <Table.Th>操作</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {table.getRowModel().rows.map((row) => (
                  <Table.Tr key={row.id}>
                    <Table.Td><Checkbox checked={selected.has(row.original.id)} onChange={() => toggleOne(row.original.id)} aria-label={`选择 ${row.original.loginIdentifier}`} /></Table.Td>
                    <Table.Td><Text ff="monospace" size="sm">{row.original.loginIdentifier}</Text></Table.Td>
                    <Table.Td><Badge color={row.original.materialStatus === 'complete' ? 'success' : 'warning'} variant="light">{row.original.materialStatus === 'complete' ? '资料已保存' : '资料待补'}</Badge></Table.Td>
                    <Table.Td><Badge color="gray" variant="light">未验证空间</Badge></Table.Td>
                    <Table.Td><Button variant="subtle" size="xs" onClick={() => setEditing(row.original)}>修正资料</Button></Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </ScrollArea>
          {editing ? (
            <Paper withBorder radius={8} p="md" bg="gray.0">
              <Stack gap="sm">
                <Text fw={600}>修正 {editing.loginIdentifier}</Text>
                <PasswordInput label="新密码" value={editPassword} onChange={(event) => setEditPassword(event.currentTarget.value)} required />
                <TextInput label="2FA" value={editTotp} onChange={(event) => setEditTotp(event.currentTarget.value)} placeholder="留空表示待补" />
                <Group justify="flex-end"><Button variant="default" onClick={() => setEditing(null)}>取消</Button><Button onClick={() => void saveCorrection()} loading={pending === 'import'} disabled={!editPassword}>保存修正</Button></Group>
              </Stack>
            </Paper>
          ) : null}
          <Group justify="space-between" align="center">
            <Text size="sm" c="dimmed">共 {total} 条 · 密码和 2FA 已隐藏</Text>
            <Stack gap={6} align="flex-end">
              <Checkbox checked={confirmExport} onChange={(event) => setConfirmExport(event.currentTarget.checked)} label={`确认导出 ${exportCount} 条资料`} disabled={exportCount < 1} />
              <Button onClick={() => void exportText()} loading={pending === 'export'} disabled={!confirmExport || exportCount < 1}>导出 TXT</Button>
            </Stack>
          </Group>
        </Stack>
      </Paper>
    </Stack>
  );
}

function ImportFeedback({ result }: { result: ImportResult }) {
  return (
    <Paper withBorder radius={8} p="sm" bg="gray.0">
      <Text size="sm">已保存 {result.imported} 条 · 重复 {result.duplicate} 条 · 无效 {result.invalid} 条</Text>
      {result.rows.some((row) => row.status !== 'imported') ? <Stack gap={4} mt="xs">{result.rows.filter((row) => row.status !== 'imported').map((row) => <ImportRow key={`${row.line}-${row.status}`} row={row} />)}</Stack> : null}
    </Paper>
  );
}

function ImportRow({ row }: { row: ImportRow }) {
  const label = row.status === 'duplicate' ? '重复' : row.status === 'needs_totp' ? '资料待补' : '无效';
  return <Text size="xs" c={row.status === 'invalid' ? 'error' : 'warning'}>第 {row.line} 行 · {label}{row.identifier ? ` · ${row.identifier}` : ''}{row.message ? ` · ${row.message}` : ''}</Text>;
}
