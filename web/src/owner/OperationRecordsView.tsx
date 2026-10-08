import ActionNotice from '../shared/ActionNotice';
import { Button, Group, Paper, Stack, Table, Text } from '@mantine/core';
import useOperationRecords from './useOperationRecords';
import { operationRecordStatus } from './operationRecordStatus';
import ListPagination from '../shared/ListPagination';
import StatusBadge from '../shared/StatusBadge';
import { formatDateTime } from '../shared/dateTime';

export default function OperationRecordsView({ active, onOpen }: { active: boolean; onOpen: (id: string) => void }) {
  const { page, pageSize, records, notice, dismissNotice, loading, readFailed, setPage, setPageSize, refresh } = useOperationRecords(active);
  return <Stack gap={16}><ActionNotice message={notice} onClose={dismissNotice} /><Paper withBorder radius={12} className="management-list-panel">
    <Group className="management-toolbar" justify="space-between"><Text fw={600}>操作记录</Text><Button variant="default" disabled={loading} onClick={refresh}>刷新</Button></Group>
    <Table.ScrollContainer minWidth={800}><Table className="management-table" aria-busy={loading}><Table.Thead><Table.Tr><Table.Th>批次</Table.Th><Table.Th>母号</Table.Th><Table.Th>空间</Table.Th><Table.Th>账号数</Table.Th><Table.Th>计划清退</Table.Th><Table.Th>状态</Table.Th><Table.Th>操作</Table.Th></Table.Tr></Table.Thead><Table.Tbody>{records?.items.length ? records.items.map((item) => <Table.Tr key={item.id}><Table.Td>{item.sourceBatchName ?? `第 ${item.sequenceNo} 批`}</Table.Td><Table.Td>{item.motherAccountName}</Table.Td><Table.Td>{item.workspaceName}</Table.Td><Table.Td>{item.targetCount}</Table.Td><Table.Td>{formatDateTime(item.plannedAt)}</Table.Td><Table.Td><StatusBadge {...operationRecordStatus(item)} /></Table.Td><Table.Td><Button variant="subtle" size="xs" onClick={() => onOpen(item.id)}>打开操作</Button></Table.Td></Table.Tr>) : <Table.Tr><Table.Td colSpan={7}><Text py={32} c="dimmed" ta="center">{loading ? '正在读取操作记录…' : readFailed ? '操作记录读取失败，请刷新重试' : '暂无操作记录'}</Text></Table.Td></Table.Tr>}</Table.Tbody></Table></Table.ScrollContainer>
    <ListPagination page={page} pageSize={pageSize} total={records?.total ?? 0} disabled={loading} onPageChange={setPage} onPageSizeChange={setPageSize} />
  </Paper></Stack>;
}
