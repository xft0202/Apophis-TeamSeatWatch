import { Box, Button, Checkbox, Group, Paper, Select, SimpleGrid, Stack, Table, Text, TextInput } from '@mantine/core';
import AccountIdentity from './AccountIdentity';
import { latestProbe } from './accountPresentation';
import { togglePage } from './childSelection';
import type { OperationSelectionState } from './useOperationSelection';
import StatusBadge from '../shared/StatusBadge';
import ListPagination from '../shared/ListPagination';
import ListSelectionBar from '../shared/ListSelectionBar';
import { formatDateTime } from '../shared/dateTime';

type Props = { selection: OperationSelectionState; onRepair: (tab: 'workspaces' | 'standby') => void };
export default function OperationSelectionView({ selection: state, onRepair }: Props) {
  const { draft, mother, mothers, source, batches, selected, selection, rows, pending, loading } = state;
  const motherOptions = new Map(mothers.filter((item) => item.status === 'active').map((item) => [item.id, { value: item.id, label: item.loginIdentifier }]));
  if (mother) motherOptions.set(mother.id, { value: mother.id, label: mother.loginIdentifier });
  const batchOptions = new Map(batches.filter((item) => item.id !== draft?.excludedSourceBatchId).map((item) => [item.id, { value: item.id, label: `${item.name} · ${item.memberCount} 个账号` }]));
  if (source) batchOptions.set(source.id, { value: source.id, label: `${source.name} · ${source.memberCount} 个账号` });
  const exact = new Set(selection?.members.map((item) => item.accountId));
  const pageIds = rows.filter((item) => exact.has(item.id)).map((item) => item.id);
  const selectedOnPage = pageIds.filter((id) => selected.has(id)).length;
  return <Stack gap={20}>
    <Paper withBorder radius={12} p={24}><SimpleGrid cols={{ base: 1, sm: 2 }} spacing={20}>
      <Box><Select label="母号" placeholder="搜索并选择母号" searchable searchValue={state.motherSearch} onSearchChange={state.setMotherSearch} filter={({ options }) => options} data={[...motherOptions.values()]} value={draft?.motherAccountId ?? null} disabled={pending || loading} nothingFoundMessage="没有匹配的母号" onChange={(value) => { if (value) void state.chooseMother(value); }} />{state.motherOptionPage * 20 < state.motherTotal ? <Button variant="subtle" size="xs" onClick={state.moreMothers}>加载更多母号</Button> : null}</Box>
      <Box><Select label="目标空间" placeholder={draft?.motherAccountId ? '选择此母号下的空间' : '先选择母号'} data={state.discovery?.workspaces.map((item) => ({ value: item.id, label: item.displayName, disabled: item.accessStatus !== 'readable' })) ?? []} value={draft?.workspaceId ?? null} disabled={pending || loading || !draft?.motherAccountId} onChange={(value) => { if (value) void state.chooseWorkspace(value); }} />{state.workspaceStatus ? <Text size="xs" mt={8} c={state.workspaceReady && state.workspaceStatus === '可以操作' ? 'success' : 'warning'} role="status">{state.workspaceStatus}</Text> : null}</Box>
      <Box><Select label="来源批次" placeholder="搜索并选择批次" searchable searchValue={state.batchSearch} onSearchChange={state.setBatchSearch} filter={({ options }) => options} data={[...batchOptions.values()]} value={source?.id ?? null} disabled={pending || loading} nothingFoundMessage="没有匹配的批次" onChange={(value) => void state.chooseBatch(value ?? '')} />{state.batchOptionPage * 20 < state.batchTotal ? <Button variant="subtle" size="xs" onClick={state.moreBatches}>加载更多批次</Button> : null}</Box>
      <TextInput type="datetime-local" label="计划清退时间" value={state.plannedAt} disabled={pending || loading} onChange={(event) => state.setPlannedAt(event.currentTarget.value)} />
    </SimpleGrid></Paper>
    <Paper withBorder radius={12} className="management-list-panel">
      <Group className="management-toolbar" justify="space-between"><Text fw={600}>本次账号</Text><Group gap={8}><Button variant="subtle" size="sm" onClick={() => onRepair('workspaces')}>空间管理</Button><Button variant="subtle" size="sm" onClick={() => onRepair('standby')}>批次管理</Button></Group></Group>
      <ListSelectionBar count={selected.size} total={state.rowTotal} disabled={pending || state.rowsLoading} onSelectAll={() => { void state.selectEntireBatch().catch(() => state.reload()); }} onClear={() => state.setSelected(new Set())} />
      <Table.ScrollContainer minWidth={650}><Table className="management-table" aria-busy={state.rowsLoading}>
        <Table.Thead><Table.Tr><Table.Th w={56}><Checkbox aria-label="选择本页账号" checked={pageIds.length > 0 && selectedOnPage === pageIds.length} indeterminate={selectedOnPage > 0 && selectedOnPage < pageIds.length} disabled={!pageIds.length || pending || state.rowsLoading} onChange={() => state.setSelected(togglePage(selected, pageIds))} /></Table.Th><Table.Th className="account-identity-cell">账号</Table.Th><Table.Th>AT</Table.Th><Table.Th>探测结果</Table.Th><Table.Th>探测时间</Table.Th></Table.Tr></Table.Thead>
        <Table.Tbody>{rows.length ? rows.map((item) => { const probe = latestProbe(item); return <Table.Tr key={item.id} data-selected={selected.has(item.id) || undefined}><Table.Td><Checkbox aria-label={`选择 ${item.identifier}`} checked={selected.has(item.id)} disabled={!exact.has(item.id) || pending || state.rowsLoading} onChange={() => { const next = new Set(selected); if (next.has(item.id)) next.delete(item.id); else next.add(item.id); state.setSelected(next); }} /></Table.Td><Table.Td className="account-identity-cell"><AccountIdentity identifier={item.identifier} /></Table.Td><Table.Td><StatusBadge tone={item.tokenStatus?.hasAccessToken ? 'success' : 'gray'} label={item.tokenStatus?.hasAccessToken ? '有 AT' : '无 AT'} /></Table.Td><Table.Td><StatusBadge tone={probe.status === 'available' ? 'success' : probe.status === 'credential_invalid' || probe.status === 'definitely_unavailable' ? 'error' : 'warning'} label={probe.label} /></Table.Td><Table.Td>{probe.at ? formatDateTime(probe.at) : '—'}</Table.Td></Table.Tr>; }) : <Table.Tr><Table.Td colSpan={5}><Text ta="center" py={32} c="dimmed" role="status">{state.rowsLoading ? '正在读取账号…' : source ? '此批次暂无账号' : '选择批次后显示本次账号'}</Text></Table.Td></Table.Tr>}</Table.Tbody>
      </Table></Table.ScrollContainer>
      {source ? <ListPagination page={state.page} pageSize={state.pageSize} total={state.rowTotal} disabled={pending || state.rowsLoading} onPageChange={state.setPage} onPageSizeChange={state.setPageSize} /> : null}
      {source && selected.size === 0 && state.rowTotal > 0 ? <Group p={16}><Button variant="subtle" onClick={() => { void state.selectEntireBatch().catch(() => state.reload()); }}>选择整批 {state.rowTotal} 个账号</Button></Group> : null}
    </Paper>
  </Stack>;
}
