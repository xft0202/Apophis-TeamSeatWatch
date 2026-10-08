import ActionNotice from '../shared/ActionNotice';
import { Box, Button, Checkbox, Divider, Group, Modal, Paper, Select, Stack, Table, Text, TextInput, Title } from '@mantine/core';
import ListPagination from '../shared/ListPagination';
import ListSelectionBar from '../shared/ListSelectionBar';
import StatusBadge from '../shared/StatusBadge';
import AccountIdentity from './AccountIdentity';
import MotherWorkspaceControls from './MotherWorkspaceControls';
import OwnerIcon from './OwnerIcon';
import { cardStateOptions, cardStateValue } from './cardManagement';
import { recordStatus, recordTime, type DeliveryRecord } from './deliveryRecords';
import { deliveryEventLabels } from './deliveryTimeline';
import { useCardManagement, type CardManagementState } from './useCardManagement';

type Props = { active?: boolean; focusBatchId?: string | null; onClearBatchScope: () => void; onRecords: (record: DeliveryRecord) => void; onChannelSettings: () => void };
function RowActions({ record, state, onRecords }: { record: DeliveryRecord; state: CardManagementState; onRecords: Props['onRecords'] }) {
  return <Group justify="center" gap={4} wrap="nowrap">
    <Button size="xs" variant="subtle" disabled={state.busy || !record.secretAvailable} title={!record.secretAvailable ? '此卡密未保存完整内容，只能查看尾号' : undefined} loading={state.copier.copyingId === record.membershipId} onClick={() => void state.copy(record)}>{state.copier.copiedId === record.membershipId ? '已复制' : '复制'}</Button>
    <Button size="xs" variant="subtle" disabled={state.busy} onClick={() => state.openDetail(record)}>详情</Button>
    {record.orderStatus === 'claimed' ? <Button size="xs" variant="subtle" disabled={state.busy} onClick={() => onRecords(record)}>兑换记录</Button> : null}
    {record.cardStatus === 'active' ? <Button size="xs" variant="subtle" color="error" disabled={state.busy} onClick={() => state.setConfirmRevoke(record)}>撤销</Button> : null}
  </Group>;
}
function CardDetail({ state, onRecords }: { state: CardManagementState; onRecords: Props['onRecords'] }) {
  const record = state.detail;
  return <Stack gap={24} className="management-page card-detail">
    <Group className="management-subheader" justify="space-between"><Group gap={12}><Box className="management-page-icon"><OwnerIcon name="cards" size={20} /></Box><Title order={2}>卡密详情</Title></Group><Button variant="default" leftSection={<OwnerIcon name="arrow-left" size={16} />} disabled={state.busy} onClick={state.closeDetail}>返回卡密管理</Button></Group>
    <ActionNotice message={state.notice} onClose={state.dismissNotice} />
    <ActionNotice message={state.feedback} tone="success" onClose={state.dismissFeedback} />
    {record ? <Paper withBorder radius={12} className="management-list-panel" aria-busy={state.detailLoading}>
      <Group className="management-toolbar" justify="space-between"><AccountIdentity identifier={record.targetIdentifier} /><StatusBadge {...recordStatus(record)} /></Group>
      <Stack p={24} gap={20}>
        <Box className="records-detail-grid">
          <Box className="detail-fact"><Text className="detail-fact-label">卡密尾号</Text><Text ff="monospace">•••• {record.cardDisplaySuffix}</Text></Box>
          <Box className="detail-fact"><Text className="detail-fact-label">来源批次</Text><Text>{record.sourceBatchName ?? `第 ${record.batchSequenceNo} 批`}</Text></Box>
          <Box className="detail-fact"><Text className="detail-fact-label">空间</Text><Text>{record.workspaceName}</Text></Box>
          <Box className="detail-fact"><Text className="detail-fact-label">生成时间</Text><Text>{recordTime(record.cardGeneratedAt)}</Text></Box>
          <Box className="detail-fact"><Text className="detail-fact-label">兑换截止</Text><Text>{recordTime(record.redemptionDeadline)}</Text></Box>
          <Box className="detail-fact"><Text className="detail-fact-label">兑换时间</Text><Text>{recordTime(record.redeemedAt)}</Text></Box>
        </Box>
        {!record.secretAvailable ? <Text size="sm" c="dimmed">此卡密未保存完整内容，只能查看原记录和尾号。</Text> : null}
        <Group justify="flex-end" gap={8}>
          <Button variant="default" disabled={state.busy || !record.secretAvailable || state.detailLoading} loading={state.copier.copyingId === record.membershipId} onClick={() => void state.copy(record)}>{state.copier.copiedId === record.membershipId ? '已复制' : '复制卡密'}</Button>
          {record.orderStatus === 'claimed' ? <Button variant="default" disabled={state.busy} onClick={() => onRecords(record)}>查看兑换记录</Button> : null}
          {record.cardStatus === 'active' ? <Button variant="outline" color="error" disabled={state.busy || state.detailLoading} onClick={() => state.setConfirmRevoke(record)}>撤销卡密</Button> : null}
        </Group><Divider />
        <Box><Text fw={600} mb={12}>操作时间线</Text><Table.ScrollContainer minWidth={500}><Table className="management-table"><Table.Thead><Table.Tr><Table.Th>时间</Table.Th><Table.Th>操作</Table.Th><Table.Th>结果</Table.Th></Table.Tr></Table.Thead><Table.Tbody>{record.timeline?.length ? record.timeline.map((event, index) => { const labels = deliveryEventLabels(event); return <Table.Tr key={`${event.occurredAt}:${index}`}><Table.Td>{recordTime(event.occurredAt)}</Table.Td><Table.Td>{labels.action}</Table.Td><Table.Td>{labels.result}</Table.Td></Table.Tr>; }) : <Table.Tr><Table.Td colSpan={3}><Text ta="center" c="dimmed" py={20}>{state.detailLoading ? '正在读取时间线' : '暂无操作记录'}</Text></Table.Td></Table.Tr>}</Table.Tbody></Table></Table.ScrollContainer></Box>
      </Stack>
    </Paper> : <Paper withBorder className="management-empty-state"><Text c="dimmed">{state.detailLoading ? '正在读取卡密详情' : '卡密详情读取失败'}</Text></Paper>}
  </Stack>;
}
export default function CardManagementView({ active = true, focusBatchId = null, onClearBatchScope, onRecords, onChannelSettings }: Props) {
  const state = useCardManagement(active, focusBatchId);
  const items = state.data?.items ?? [], total = state.data?.total ?? 0;
  const selectedOnPage = items.filter((item) => state.selected.has(item.membershipId)).length;
  return <>
    {state.detailId ? <CardDetail state={state} onRecords={onRecords} /> : <Stack gap={24} className="management-page cards-page">
      <Group className="management-subheader" justify="space-between"><Group gap={12}><Box className="management-page-icon"><OwnerIcon name="cards" size={20} /></Box><Title order={2}>卡密管理</Title></Group><Group gap={8}><Button variant="subtle" color="gray" disabled={state.busy} onClick={onChannelSettings}>渠道设置</Button><Button variant="default" disabled={!state.selected.size || state.busy || state.selecting} loading={state.pending === 'export'} onClick={() => void state.download()}>导出卡密</Button><Button variant="default" disabled={state.loading || state.busy} onClick={state.refresh}>刷新</Button></Group></Group>
      <ActionNotice message={state.notice} onClose={state.dismissNotice} />
      <ActionNotice message={state.feedback} tone="success" onClose={state.dismissFeedback} />
      <Paper withBorder radius={12} className="management-list-panel">
        <MotherWorkspaceControls scope={state.scope} disabled={state.busy || state.selecting} {...(focusBatchId ? { onChange: onClearBatchScope } : {})} />
        <Group className="management-toolbar cards-filters" gap={12} align="end">
          <TextInput className="management-search" aria-label="搜索卡密" placeholder="账号、批次名称、空间或卡密尾号" leftSection={<OwnerIcon name="search" size={16} />} value={state.search} disabled={!state.ready || state.busy} onChange={(event) => state.setSearch(event.currentTarget.value)} />
          <Select aria-label="卡密状态" placeholder="全部状态" clearable data={cardStateOptions} value={state.state ?? null} disabled={!state.ready || state.busy} onChange={(value) => state.setState(cardStateValue(value))} />
          <Button variant="subtle" color="gray" disabled={state.busy || (!state.search && !state.state)} onClick={state.resetFilters}>重置</Button>
          {focusBatchId ? <Group gap={8}><StatusBadge tone="indigo" label="本轮卡密" /><Button variant="subtle" size="xs" disabled={state.busy} onClick={onClearBatchScope}>查看此空间全部卡密</Button></Group> : null}
        </Group>
        <ListSelectionBar noun="卡密" unit="张" count={state.selected.size} total={total} allSelected={state.allFilteredSelected} disabled={state.busy || state.loading} selecting={state.selecting} onSelectAll={() => void state.selectAll()} onClear={state.clearSelection} />
        <Table.ScrollContainer minWidth={1200}><Table className="management-table card-list-table" aria-busy={state.loading}>
          <Table.Thead><Table.Tr><Table.Th w={56}><Checkbox aria-label="选择本页卡密" disabled={state.loading || state.busy || state.selecting || !items.length} checked={items.length > 0 && selectedOnPage === items.length} indeterminate={selectedOnPage > 0 && selectedOnPage < items.length} onChange={() => state.toggle(items, selectedOnPage === items.length)} /></Table.Th><Table.Th className="account-identity-cell">账号</Table.Th><Table.Th>卡密尾号</Table.Th><Table.Th>来源批次</Table.Th><Table.Th>空间</Table.Th><Table.Th>状态</Table.Th><Table.Th>生成时间</Table.Th><Table.Th>兑换截止</Table.Th><Table.Th>操作</Table.Th></Table.Tr></Table.Thead>
          <Table.Tbody>{items.length ? items.map((item) => <Table.Tr key={item.membershipId} data-selected={state.selected.has(item.membershipId) || undefined}>
            <Table.Td><Checkbox aria-label={`选择 ${item.targetIdentifier} 的卡密`} checked={state.selected.has(item.membershipId)} disabled={state.busy || state.loading || state.selecting} onChange={() => state.toggle([item], state.selected.has(item.membershipId))} /></Table.Td>
            <Table.Td className="account-identity-cell"><AccountIdentity identifier={item.targetIdentifier} /></Table.Td><Table.Td><Text size="sm" ff="monospace">•••• {item.cardDisplaySuffix}</Text></Table.Td><Table.Td><Text size="sm">{item.sourceBatchName ?? `第 ${item.batchSequenceNo} 批`}</Text></Table.Td><Table.Td><Text size="sm">{item.workspaceName}</Text></Table.Td><Table.Td><StatusBadge {...recordStatus(item)} /></Table.Td><Table.Td><Text size="xs">{recordTime(item.cardGeneratedAt)}</Text></Table.Td><Table.Td><Text size="xs">{recordTime(item.redemptionDeadline)}</Text></Table.Td><Table.Td><RowActions record={item} state={state} onRecords={onRecords} /></Table.Td>
          </Table.Tr>) : <Table.Tr><Table.Td colSpan={9}><Box className="management-empty-state"><OwnerIcon name="cards" size={24} /><Text c="dimmed" role="status">{state.loading ? '正在读取卡密' : !state.ready ? '选择母号和空间后查看卡密' : state.readFailed ? '卡密读取失败，请刷新重试' : '暂无卡密记录'}</Text></Box></Table.Td></Table.Tr>}</Table.Tbody>
        </Table></Table.ScrollContainer>
        <ListPagination page={state.page} pageSize={state.pageSize} total={total} disabled={!state.ready || state.loading || state.busy || state.selecting} onPageChange={state.setPage} onPageSizeChange={state.setPageSize} />
      </Paper>
    </Stack>}
    <Modal opened={Boolean(state.confirmRevoke)} onClose={() => { if (!state.busy) state.setConfirmRevoke(null); }} title="撤销卡密" centered closeOnEscape={!state.busy} closeOnClickOutside={!state.busy} withCloseButton={!state.busy}>
      <Stack gap={16}><Text fw={600}>{state.confirmRevoke?.targetIdentifier}</Text><Text size="sm" c="dimmed">{state.confirmRevoke?.workspaceName} · •••• {state.confirmRevoke?.cardDisplaySuffix}</Text><Text size="sm">撤销后此卡密无法兑换，已发出的访问凭据也会失效。</Text><Group justify="flex-end"><Button variant="default" disabled={state.busy} onClick={() => state.setConfirmRevoke(null)}>取消</Button><Button color="error" loading={Boolean(state.pending?.startsWith('revoke:'))} onClick={() => void state.revoke()}>确认撤销</Button></Group></Stack>
    </Modal>
  </>;
}
