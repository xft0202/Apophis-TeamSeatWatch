import { Alert, Button, Checkbox, Group, Pagination, Paper, Radio, Select, Stack, Table, Text, TextInput } from '@mantine/core';
import { useEffect, useRef, useState } from 'react';
import { exportChildMaterials, listChildMaterials, ownerProblem } from './auth';
import { standbyApi, type Batch, type Scope, type Selection } from './standbyBatches';
import { togglePage } from './childSelection';
import { frozenStandbyPreview, standbyRangeLimitNotice } from './standbySelection';
import type { components } from '../../generated/owner';

type Child = components['schemas']['TargetAccount'];
type Frozen = { selection: Selection; batch: Batch | undefined; name: string; action: 'add' | 'remove' | 'export' };

export default function StandbyChildBatchesView() {
  const [batches, setBatches] = useState<Batch[]>([]);
  const [batchId, setBatchId] = useState<string | null>(null);
  const [name, setName] = useState('');
  const [action, setAction] = useState<'add' | 'remove' | 'export'>('add');
  const [scope, setScope] = useState<Scope>('selected');
  const [search, setSearch] = useState('');
  const [page, setPage] = useState(1);
  const [items, setItems] = useState<Child[]>([]);
  const [total, setTotal] = useState(0);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [frozen, setFrozen] = useState<Frozen | null>(null);
  const [confirmed, setConfirmed] = useState(false);
  const [pending, setPending] = useState(false);
  const [loading, setLoading] = useState(true);
  const [notice, setNotice] = useState('');
  const [revision, setRevision] = useState(0);
  const generation = useRef(0);
  const batch = batches.find((item) => item.id === batchId);
  const pageIds = items.map((item) => item.id);
  const pageSelected = pageIds.length > 0 && pageIds.every((id) => selected.has(id));
  const invalidate = () => { generation.current += 1; setFrozen(null); setConfirmed(false); };

  useEffect(() => {
    let active = true;
    void standbyApi.list().then((result) => { if (active) setBatches(result); }).catch(() => { if (active) setNotice('待用批次无法加载。'); });
    return () => { active = false; };
  }, [revision]);
  useEffect(() => {
    let active = true;
    setLoading(true); setItems([]); invalidate();
    void listChildMaterials(page, search).then((result) => {
      if (active) { setItems(result.items); setTotal(result.total); setLoading(false); }
    }).catch(() => { if (active) { setLoading(false); setNotice('子号池无法加载。'); } });
    return () => { active = false; };
  }, [page, search, revision]);

  async function preview() {
    if (loading || pending || (scope === 'selected' && selected.size === 0) || (scope === 'batch' && !batch) || (action === 'remove' && !batch)) return;
    setPending(true); setNotice(''); invalidate();
    const requestGeneration = generation.current;
    try {
      const selection = await frozenStandbyPreview(() => standbyApi.preview(scope, [...selected].sort(), search, batch?.id), requestGeneration, () => generation.current);
      if (!selection) return;
      if (selection.count === 0 && action !== 'add') { setNotice('当前范围为空。'); return; }
      setFrozen({ selection, batch, name: name.trim(), action });
    } catch (error: unknown) {
      if (generation.current === requestGeneration) {
        const problem = ownerProblem(error);
        setNotice(problem.code === 'range_limit_exceeded'
          ? standbyRangeLimitNotice(problem.actualCount, 'preview')
          : problem.status === 409 ? '所选账号已变化，请刷新。' : '无法确认范围，请重试。');
      }
    } finally { setPending(false); }
  }

  async function execute() {
    if (!frozen || !confirmed || pending || loading) return;
    setPending(true); setNotice('');
    try {
      if (frozen.action === 'export') {
        const content = frozen.selection.scope === 'batch'
          ? await standbyApi.export(frozen.batch!, frozen.selection)
          : await exportChildMaterials('selected', frozen.selection.members.map((member) => member.accountId), '', frozen.selection.count);
        const url = URL.createObjectURL(new Blob([content], { type: 'text/plain;charset=utf-8' }));
        const link = document.createElement('a'); link.href = url; link.download = 'child-account-materials.txt'; link.click(); URL.revokeObjectURL(url);
        setNotice(`已导出 ${frozen.selection.count} 个账号的资料。`);
      } else {
        const saved = await standbyApi.save(frozen.name, frozen.selection, frozen.batch, frozen.action);
        setBatchId(saved.id); setName(saved.name); setSelected(new Set());
        setNotice(`已保存批次 ${saved.name}（${saved.memberCount} 个去重账号）。`);
      }
      invalidate(); setRevision((value) => value + 1);
    } catch (error: unknown) {
      invalidate();
      const problem = ownerProblem(error);
      setNotice(problem.code === 'range_limit_exceeded'
        ? standbyRangeLimitNotice(problem.actualCount, 'save')
        : problem.status === 409 ? '批次或账号归属已变化，未保存或导出；请重新预览确认。' : '操作失败，请重试。');
    } finally { setPending(false); }
  }

  return <Stack gap="lg">
    {notice ? <Alert color="indigo">{notice}</Alert> : null}
    <Text size="sm" c="dimmed">待用批次仅整理账号，不绑定空间、不邀请、不交付；一个子号可同时属于多个空间，但当前只归属一个待用批次。成员数不代表目标空间可用席位。每个批次及单次冻结范围硬上限均为 10000 个账号；当前筛选超过上限时请缩小条件，不能仅处理前 10000 个。</Text>
    <Group align="end"><Select label="待用批次" placeholder="新建批次" clearable data={batches.map((item) => ({ value: item.id, label: `${item.name} · ${item.memberCount} 个账号 / ${item.domainCount} 个域名（${item.domains.join('、') || '无'}）` }))} value={batchId} onChange={(value) => { const next = batches.find((item) => item.id === value); setBatchId(value); setName(next?.name ?? ''); setAction('add'); invalidate(); }} />
      <TextInput label="批次名称" maxLength={120} value={name} onChange={(event) => { setName(event.currentTarget.value); invalidate(); }} /></Group>
    <Group><TextInput label="筛选账号或域名" value={search} onChange={(event) => { setSearch(event.currentTarget.value); setPage(1); setSelected(new Set()); invalidate(); }} /><Button variant="light" onClick={() => { invalidate(); setRevision((value) => value + 1); }}>刷新</Button></Group>
    <Paper withBorder p="sm"><Stack gap="sm"><Table striped withTableBorder><Table.Thead><Table.Tr><Table.Th><Checkbox aria-label="选择本页" checked={pageSelected} disabled={loading} onChange={() => { setSelected(togglePage(selected, pageIds)); invalidate(); }} /></Table.Th><Table.Th>账号</Table.Th><Table.Th>域名</Table.Th></Table.Tr></Table.Thead><Table.Tbody>{items.map((item) => <Table.Tr key={item.id}><Table.Td><Checkbox aria-label={`选择 ${item.identifier}`} checked={selected.has(item.id)} disabled={loading} onChange={() => { const next = new Set(selected); if (next.has(item.id)) next.delete(item.id); else next.add(item.id); setSelected(next); invalidate(); }} /></Table.Td><Table.Td>{item.identifier}</Table.Td><Table.Td>{item.identifier.split('@')[1]}</Table.Td></Table.Tr>)}</Table.Tbody></Table>
      <Group justify="space-between"><Text size="sm">当前筛选 {total} · 跨页已选 {selected.size} · 第 {page} 页</Text><Pagination total={Math.max(1, Math.ceil(total / 20))} value={page} onChange={(value) => { setPage(value); invalidate(); }} /></Group></Stack></Paper>
    <Radio.Group label="明确操作范围" value={scope} onChange={(value) => { setScope(value as Scope); invalidate(); }}><Group mt="xs"><Radio value="selected" label={`跨页已选 ${selected.size}`} /><Radio value="filtered" label={`当前筛选全部 ${total}`} /><Radio value="batch" disabled={!batch} label={`当前批次全部 ${batch?.memberCount ?? 0}`} /></Group></Radio.Group>
    <Group><Select label="操作" value={action} data={[{ value: 'add', label: batch ? '加入 / 移入批次' : '创建批次' }, { value: 'remove', label: '从批次移出', disabled: !batch }, { value: 'export', label: '导出当前范围 TXT' }]} onChange={(value) => { setAction(value as 'add' | 'remove' | 'export'); invalidate(); }} /><Button variant="light" onClick={() => void preview()} loading={pending} disabled={loading || action !== 'export' && !name.trim() || action === 'remove' && !batch || scope === 'batch' && !batch}>预览冻结范围</Button></Group>
    {frozen ? <Paper withBorder p="md"><Stack gap="sm"><Text size="sm">{frozen.action === 'export' ? '导出纯资料 TXT' : frozen.action === 'remove' ? '移出' : '分入'} · {frozen.action === 'export' && frozen.selection.scope !== 'batch' ? '子号资料池' : frozen.batch?.name || frozen.name} · <strong>{frozen.selection.count}</strong> 个去重账号 · {frozen.selection.scope === 'filtered' ? '当前筛选全部（非当前页）' : frozen.selection.scope === 'batch' ? '整个当前批次' : '跨页所选'}</Text><Checkbox label={`确认此冻结范围恰为 ${frozen.selection.count} 个账号`} checked={confirmed} onChange={(event) => setConfirmed(event.currentTarget.checked)} /><Button onClick={() => void execute()} disabled={!confirmed || frozen.action === 'export' && frozen.selection.count === 0} loading={pending}>确认执行</Button></Stack></Paper> : null}
  </Stack>;
}
