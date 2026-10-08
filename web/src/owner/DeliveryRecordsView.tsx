import ActionNotice from '../shared/ActionNotice';
import { Box, Button, Divider, Group, Paper, Select, Stack, Table, Text, TextInput, Title } from '@mantine/core';
import type { ReactNode } from 'react';
import ListPagination from '../shared/ListPagination';
import StatusBadge from '../shared/StatusBadge';
import AccountIdentity from './AccountIdentity';
import MotherWorkspaceControls from './MotherWorkspaceControls';
import OwnerIcon from './OwnerIcon';
import { recordTime } from './deliveryRecords';
import { deliveryEventLabels } from './deliveryTimeline';
import { credentialLabels, credentialOptions, credentialValue, oauthStatus, reclaimLabels, reclaimOptions, reclaimOrigin, reclaimStage, reclaimValue, versionLabel, type RedemptionFocus } from './redemptionRecords';
import { useRedemptionRecords, type RedemptionRecordsState } from './useRedemptionRecords';

type Props = { active?: boolean; focus?: RedemptionFocus | null; onClearFocus: () => void; onBackToCards: () => void };
function Fact({ label, children }: { label: string; children: ReactNode }) {
  return <Box className="detail-fact"><Text className="detail-fact-label">{label}</Text>{typeof children === 'string' ? <Text size="sm">{children}</Text> : children}</Box>;
}
function RecordDetail({ state, focus, onBackToCards }: { state: RedemptionRecordsState; focus: Props['focus']; onBackToCards: Props['onBackToCards'] }) {
  const detail = state.detail, record = detail?.record;
  return <Stack gap={24} className="management-page records-detail">
    <Group className="management-subheader" justify="space-between">
      <Group gap={12}><Box className="management-page-icon"><OwnerIcon name="redemptions" size={20} /></Box><Title order={2}>兑换详情</Title></Group>
      <Group gap={8}>{focus ? <Button variant="subtle" disabled={state.pending} onClick={onBackToCards}>返回卡密管理</Button> : null}<Button variant="default" disabled={state.pending || state.detailLoading || state.scope.scopeLoading} onClick={state.refresh}>刷新</Button><Button variant="default" leftSection={<OwnerIcon name="arrow-left" size={16} />} disabled={state.pending} onClick={state.closeDetail}>返回兑换记录</Button></Group>
    </Group>
    <ActionNotice message={state.notice} onClose={state.dismissNotice} />
    <ActionNotice message={state.feedback} tone="success" onClose={state.dismissFeedback} />
    {!detail && (state.detailLoading || state.scope.scopeLoading) ? <Paper withBorder className="management-empty-state" role="status">正在读取兑换详情</Paper> : null}
    {detail && record ? <Paper withBorder radius={12} className="management-list-panel" aria-busy={state.detailLoading}>
      <Group className="management-toolbar" justify="space-between"><AccountIdentity identifier={record.targetIdentifier} /><Group gap={8}><StatusBadge label="已兑换" tone="success" /><StatusBadge {...credentialLabels[record.credentialState]} /></Group></Group>
      <Stack p={24} gap={24}>
        <Box><Title order={3} className="redemption-section-title">兑换对象</Title><Box className="records-detail-grid">
          <Fact label="账号">{record.targetIdentifier}</Fact><Fact label="母号">{record.motherIdentifier}</Fact>
          <Fact label="空间">{record.workspaceName}</Fact><Fact label="来源批次">{record.sourceBatchName ?? '未留存'}</Fact>
          <Fact label="执行本轮">{`第 ${record.batchSequenceNo} 轮`}</Fact><Fact label="卡密尾号">{`•••• ${record.cardDisplaySuffix}`}</Fact>
          <Fact label="兑换时间">{recordTime(record.redeemedAt)}</Fact><Fact label="卡密生成时间">{recordTime(record.cardGeneratedAt)}</Fact>
        </Box></Box>
        <Divider />
        <Box><Title order={3} className="redemption-section-title">原始交付</Title><Box className="records-detail-grid">
          <Fact label="兑换时的交付版本">{versionLabel(detail.originalDelivery?.generation)}</Fact><Fact label="原始交付生成时间">{recordTime(detail.originalDelivery?.createdAt)}</Fact>
          <Fact label="初次兑换结果"><StatusBadge label="已成功兑换" tone="success" /></Fact><Fact label="原账号与空间">{`${record.targetIdentifier} · ${record.workspaceName}`}</Fact>
        </Box>{!detail.originalDelivery ? <Text size="sm" c="dimmed" mt={12}>原始交付版本未留存，无法确认兑换时的版本。</Text> : null}</Box>
        <Divider />
        <Box><Title order={3} className="redemption-section-title">当前状态</Title><Box className="records-detail-grid">
          <Fact label="当前交付版本">{versionLabel(detail.currentDelivery?.generation)}</Fact><Fact label="当前交付保存时间">{recordTime(detail.currentDelivery?.createdAt)}</Fact>
          <Fact label="凭据状态"><StatusBadge {...credentialLabels[record.credentialState]} /></Fact><Fact label="客户当前可领取"><StatusBadge label={record.accessAvailable ? '可领取' : '不可领取'} tone={record.accessAvailable ? 'success' : 'gray'} /></Fact>
          <Fact label="最近核验时间">{recordTime(record.probedAt)}</Fact><Fact label="账号登录交付">{oauthStatus(record.assetStatus)}</Fact>
          <Fact label="服务状态"><StatusBadge label={record.serviceEnded ? '已结束' : '服务中'} tone={record.serviceEnded ? 'gray' : 'success'} /></Fact><Fact label="卡密状态"><StatusBadge label={record.cardRevoked ? '已撤销' : '有效'} tone={record.cardRevoked ? 'error' : 'success'} /></Fact>
        </Box></Box>
        <Divider />
        <Box role="region" aria-label="401找回历史"><Group justify="space-between" align="center" mb={16}><Title order={3} className="redemption-section-title" mb={0}>401 找回记录</Title><Group gap={12}><Box role="status" aria-label="最近401找回结果"><StatusBadge {...reclaimLabels[record.reclaimState]} /></Box>{record.canAuthorizeReclaim ? <Button variant="default" loading={state.pending} disabled={state.pending || state.detailLoading} onClick={() => void state.authorize()}>重新授权找回</Button> : null}</Group></Group>
          <Box className="records-detail-grid" mb={16}><Fact label="最近发起时间">{recordTime(record.reclaimRequestedAt)}</Fact><Fact label="发起来源">{reclaimOrigin(record.reclaimOrigin)}</Fact><Fact label="当前阶段">{reclaimStage(record.reclaimStage)}</Fact><Fact label="最近更新时间">{recordTime(record.reclaimUpdatedAt)}</Fact></Box>
          {record.cardRevoked || record.serviceEnded ? <Text size="sm" c="dimmed" mb={16}>{record.cardRevoked ? '卡密已撤销，不能继续找回。' : '服务已结束，不能继续找回。'}</Text> : record.reclaimState === 'pending_check' ? <Text size="sm" c="dimmed" mb={16}>找回结果待核验，可刷新查看最新记录。</Text> : null}
          {detail.reclaims.items.length ? <Table.ScrollContainer minWidth={800}><Table className="management-table redemption-history-table"><Table.Thead><Table.Tr><Table.Th>发起时间</Table.Th><Table.Th>来源</Table.Th><Table.Th>处理阶段</Table.Th><Table.Th>找回结果</Table.Th><Table.Th>交付版本变化</Table.Th><Table.Th>更新时间</Table.Th></Table.Tr></Table.Thead><Table.Tbody>{detail.reclaims.items.map((item) => <Table.Tr key={item.id}><Table.Td>{recordTime(item.requestedAt)}</Table.Td><Table.Td>{reclaimOrigin(item.origin)}</Table.Td><Table.Td>{reclaimStage(item.stage)}</Table.Td><Table.Td><StatusBadge {...reclaimLabels[item.state]} /></Table.Td><Table.Td><Text size="sm">{versionLabel(item.previousGeneration)} → {item.replacementGeneration ? versionLabel(item.replacementGeneration) : item.state === 'healthy' ? '原凭据继续使用' : item.state === 'queued' || item.state === 'running' ? '处理中' : '未生成新版本'}</Text></Table.Td><Table.Td>{recordTime(item.updatedAt)}</Table.Td></Table.Tr>)}</Table.Tbody></Table></Table.ScrollContainer> : <Text size="sm" c="dimmed">尚未发起 401 找回</Text>}
          {detail.reclaims.total > 20 ? <ListPagination page={state.historyPage} pageSize={state.historySize} total={detail.reclaims.total} disabled={state.detailLoading || state.pending} onPageChange={state.setHistoryPage} onPageSizeChange={state.setHistorySize} /> : null}
        </Box>
        <Divider />
        <Box role="region" aria-label="兑换完整过程"><Title order={3} className="redemption-section-title">完整过程</Title><ol className="redemption-timeline">{detail.timeline.items.map((event, index) => {
          const labels = deliveryEventLabels(event);
          return <li key={`${event.occurredAt}:${event.action}:${index}`}><Box className="redemption-timeline-marker" aria-hidden="true" /><Box><Group justify="space-between" gap={8}><Text size="sm" fw={600}>{labels.action}{event.generation ? ` · 第 ${event.generation} 版` : ''}</Text><Text size="xs" c="dimmed">{recordTime(event.occurredAt)}</Text></Group><Group gap={12} mt={4}><Text size="sm">{labels.result}</Text><Text size="xs" c="dimmed">{reclaimOrigin(event.origin ?? undefined)}</Text></Group></Box></li>;
        })}</ol>{detail.timeline.total > 20 ? <ListPagination page={state.timelinePage} pageSize={state.timelineSize} total={detail.timeline.total} disabled={state.detailLoading || state.pending} onPageChange={state.setTimelinePage} onPageSizeChange={state.setTimelineSize} /> : null}</Box>
      </Stack>
    </Paper> : null}
  </Stack>;
}
export default function DeliveryRecordsView({ active = true, focus = null, onClearFocus, onBackToCards }: Props) {
  const state = useRedemptionRecords(active, focus);
  if (state.selectedId) return <RecordDetail state={state} focus={focus} onBackToCards={onBackToCards} />;
  const items = state.data?.items ?? [], filters = state.filters;
  return <Stack gap={24} className="management-page records-page">
    <Group className="management-subheader" justify="space-between"><Group gap={12}><Box className="management-page-icon"><OwnerIcon name="redemptions" size={20} /></Box><Title order={2}>兑换记录</Title></Group><Group gap={8}>{focus ? <Button variant="subtle" onClick={onBackToCards}>返回卡密管理</Button> : null}<Button variant="default" disabled={state.loading} onClick={state.refresh}>刷新</Button></Group></Group>
    <ActionNotice message={state.notice} onClose={state.dismissNotice} />
    <Paper withBorder radius={12} className="management-list-panel">
      <MotherWorkspaceControls scope={state.scope} disabled={state.pending} onChange={() => { state.scopeChanged(); onClearFocus(); }} />
      {focus && state.ready ? <Group className="management-selection-bar" justify="space-between"><Text size="sm">已定位所选账号的兑换记录</Text><Button size="xs" variant="subtle" onClick={onClearFocus}>查看此空间全部兑换记录</Button></Group> : null}
      <Box className="management-toolbar redemption-filters">
        <TextInput className="management-search" aria-label="搜索兑换记录" placeholder="搜索账号、卡密尾号或批次" leftSection={<OwnerIcon name="search" size={16} />} disabled={!state.ready} value={filters.search} onChange={(event) => state.update({ search: event.currentTarget.value })} />
        <Box className="redemption-batch-filter"><Select aria-label="来源批次" placeholder="全部来源批次" searchable clearable disabled={!state.ready} filter={({ options }) => options} value={filters.sourceId} data={state.batchOptions} searchValue={state.batchSearch} onSearchChange={state.searchBatches} onDropdownOpen={() => state.searchBatches('')} nothingFoundMessage={state.batchLoading ? '正在读取批次' : '没有匹配的来源批次'} onChange={(value) => state.update({ sourceId: value, sourceName: state.batchOptions.find((item) => item.value === value)?.label ?? '' })} />{state.hasMoreBatches ? <Button variant="subtle" size="xs" disabled={state.batchLoading} onClick={state.moreBatches}>加载更多批次</Button> : null}</Box>
        <Select aria-label="当前凭据状态" placeholder="全部凭据状态" disabled={!state.ready} clearable data={credentialOptions} value={filters.credential ?? null} onChange={(value) => state.update({ credential: credentialValue(value) })} />
        <Select aria-label="401找回状态" placeholder="全部找回状态" disabled={!state.ready} clearable data={reclaimOptions} value={filters.reclaim ?? null} onChange={(value) => state.update({ reclaim: reclaimValue(value) })} />
        <Group gap={8} wrap="nowrap" className="redemption-date-range"><Text size="xs" c="dimmed" className="redemption-date-label">兑换日期</Text><TextInput type="date" aria-label="兑换开始日期" title="兑换开始日期" disabled={!state.ready} value={filters.from} onChange={(event) => state.update({ from: event.currentTarget.value })} /><Text size="sm" c="dimmed">至</Text><TextInput error={state.invalidDates ? '结束日期不能早于开始日期' : undefined} type="date" aria-label="兑换结束日期" title="兑换结束日期" disabled={!state.ready} value={filters.to} onChange={(event) => state.update({ to: event.currentTarget.value })} /></Group>
        <Button variant="subtle" color="gray" disabled={!state.ready || (!filters.search && !filters.sourceId && !filters.credential && !filters.reclaim && !filters.from && !filters.to)} onClick={state.resetFilters}>重置</Button>
      </Box>
      <Table.ScrollContainer minWidth={1120}><Table aria-busy={state.loading} className="management-table redemption-list-table"><Table.Thead><Table.Tr><Table.Th>兑换时间</Table.Th><Table.Th className="account-identity-cell">账号</Table.Th><Table.Th>卡密尾号</Table.Th><Table.Th>来源批次 / 本轮</Table.Th><Table.Th>空间</Table.Th><Table.Th>当前凭据</Table.Th><Table.Th>最近找回</Table.Th><Table.Th>操作</Table.Th></Table.Tr></Table.Thead><Table.Tbody>{items.map((item) => <Table.Tr key={item.membershipId}><Table.Td><Text size="xs">{recordTime(item.redeemedAt)}</Text></Table.Td><Table.Td className="account-identity-cell"><AccountIdentity identifier={item.targetIdentifier} /></Table.Td><Table.Td><Text size="sm" ff="monospace">•••• {item.cardDisplaySuffix}</Text></Table.Td><Table.Td><Text size="sm">{item.sourceBatchName ?? '未留存'}</Text><Text size="xs" c="dimmed">第 {item.batchSequenceNo} 轮</Text></Table.Td><Table.Td><Text size="sm">{item.workspaceName}</Text></Table.Td><Table.Td><StatusBadge {...credentialLabels[item.credentialState]} /></Table.Td><Table.Td><StatusBadge {...reclaimLabels[item.reclaimState]} /></Table.Td><Table.Td><Button size="xs" variant="subtle" onClick={() => state.openDetail(item)}>详情</Button></Table.Td></Table.Tr>)}</Table.Tbody></Table></Table.ScrollContainer>
      {state.loading ? <Text className="management-empty-state" role="status">正在读取兑换记录</Text> : !items.length ? <Box className="management-empty-state"><OwnerIcon name="redemptions" size={24} /><Text c="dimmed">{!state.ready ? '选择母号和空间' : state.invalidDates ? '请修正兑换日期范围' : state.readFailed ? '兑换记录读取失败，请刷新重试' : '暂无兑换记录'}</Text></Box> : null}
      <ListPagination page={filters.page} pageSize={filters.pageSize} total={state.data?.total ?? 0} disabled={state.loading || !state.ready} onPageChange={(page) => state.update({ page })} onPageSizeChange={(pageSize) => state.update({ pageSize })} />
    </Paper>
  </Stack>;
}
