import ActionNotice from '../shared/ActionNotice';
import { invitationBlockers } from './operationWizardState';
import JoinResult from './JoinResult';
import WorkspaceSubscriptionNotice from './WorkspaceSubscriptionNotice';
import { oauthDiagnostic } from './oauthDiagnostics';
import TaskConcurrencyControl from './TaskConcurrencyControl';
import { Box, Button, Group, Paper, Stack, Table, Text } from '@mantine/core';
import ListPagination from '../shared/ListPagination';
import StatusBadge, { type StatusTone } from '../shared/StatusBadge';
import { useRecordCopy } from '../shared/useRecordCopy';
import AccountIdentity from './AccountIdentity';
import { seatTypeLabel } from './seatTypes';
import { accountCardIntent } from './accountCards';
import type { BatchOperationState } from './useBatchOperation';
import type { OperationStep } from './operationWizardState';
import type { JoinOperation } from './batchOperations';

type OperationTarget = JoinOperation['targets'][number];

function oauthRunning(target: OperationTarget, started: boolean) {
  return ['queued', 'retry_wait', 'running'].includes(target.delivery?.loginTaskStatus ?? '') || started && ['running', 'queued'].includes(target.status);
}

function OAuthResult({ target, started, diagnostic = false }: { target: OperationTarget; started: boolean; diagnostic?: boolean }) {
  if (!target.invitationConfirmedAt) return <Stack gap={4} align="center"><StatusBadge tone="gray" label="等待邀请" />{diagnostic ? <JoinResult target={target} phase="invitation" /> : null}</Stack>;
  const item = target.delivery;
  const running = oauthRunning(target, started);
  const failed = item?.status === 'unavailable' || ['blocked', 'unknown', 'failed'].includes(target.status);
  return <Stack gap={4} align="center">
    <StatusBadge tone={item?.status === 'ready' ? 'success' : running ? 'indigo' : failed ? 'warning' : 'gray'} label={item?.status === 'ready' ? '登录成功' : running ? '登录中' : failed ? '登录待处理' : '待登录'} />
    {diagnostic && !running ? <OAuthDiagnostic target={target} /> : null}
  </Stack>;
}

function OAuthDiagnostic({ target }: { target: OperationTarget }) {
  if (target.delivery?.unavailableReason) return <Text size="sm">{oauthDiagnostic(target.delivery.unavailableReason) ?? 'OAuth 登录未完成，请重试'}</Text>;
  return target.diagnosticCode ? <JoinResult target={target} phase="oauth" /> : null;
}

function invitationStatus(target: { status: string; invitationConfirmedAt?: string | undefined; diagnosticCode?: string | undefined }): { tone: StatusTone; label: string } {
  if (target.invitationConfirmedAt) return { tone: 'success', label: '邀请已确认' };
  if (target.diagnosticCode === 'premium_capacity_exceeded') return { tone: 'warning', label: '等待席位' };
  if (target.status === 'running') return { tone: 'indigo', label: '邀请处理中' };
  if (target.status === 'queued') return { tone: 'gray', label: '等待发送' };
  if (target.status === 'failed') return { tone: 'error', label: '邀请未完成' };
  if (target.status === 'blocked' || target.status === 'unknown') return { tone: 'warning', label: '邀请待核验' };
  return { tone: 'gray', label: '未发送' };
}

export default function BatchExecutionView({ task, step, onOpenOriginal, onReturnToOAuth }: { task: BatchOperationState; step: OperationStep; onOpenOriginal: (id: string) => void; onReturnToOAuth: () => void }) {
  const copier = useRecordCopy();
  const { operationBatch: batch, detail, joinOperation: invitation, deliveries, pending, loading } = task;
  const editable = ['joining', 'serving'].includes(batch?.status ?? '') && !pending && !loading;
  const canJoin = task.progress.invited && ['joining', 'serving'].includes(batch?.status ?? '') && !pending && !loading && !invitation?.activeTaskCount;
  const targets = invitation?.targets ?? [];
  const cardHeading = step === 3 || step === 4;
  const columnCount = step <= 2 ? 4 : 5;
  const blockers = invitationBlockers(task.joinPreview);
  return <Stack gap={16}>
    {step === 1 && batch?.motherAccountId && batch.workspaceId ? <WorkspaceSubscriptionNotice key={`${batch.motherAccountId}:${batch.workspaceId}`} motherAccountId={batch.motherAccountId} workspaceId={batch.workspaceId} preview={task.joinPreview} /> : null}
    <ActionNotice message={copier.notice} onClose={copier.dismissNotice} />
    <Paper withBorder radius={12} className="management-list-panel">
      <Group className="management-toolbar" justify="space-between"><Text fw={600}>{step === 0 ? '本次账号' : step === 1 ? '发送邀请' : step === 2 ? 'OAuth 登录' : step === 3 ? '生成卡密' : '本轮账号结果'}</Text>
        {step === 1 ? <Group gap={8}>
          <TaskConcurrencyControl value={task.taskConcurrency.concurrency} limit={task.taskConcurrency.limit} disabled={!task.taskConcurrency.ready || Boolean(invitation) || Boolean(pending)} onChange={task.taskConcurrency.setConcurrency} />
          {!invitation ? <Button disabled={!task.joinPreview?.canProceed || loading || Boolean(pending)} loading={pending === 'invite'} onClick={() => void task.invite()}>{task.joinPreview?.newInvitationCount && task.joinPreview.availablePremiumSeats === undefined ? '席位信息待同步' : task.joinPreview?.waitingSeatCount ? task.joinPreview.plannedInvitationCount ? `发送 ${task.joinPreview.plannedInvitationCount} 个邀请` : '暂无可邀请席位' : '发送全部邀请'}</Button> : <>
            <Text size="sm">邀请已确认 {invitation.invitationConfirmedCount} / {batch?.targetCount ?? 0}</Text>
            {!task.progress.allInvited && !invitation.activeTaskCount ? <>
              {invitation.retryableInvitationCount > 0 ? <Button variant="default" loading={pending === 'retry'} disabled={Boolean(pending) || loading || !task.joinPreview?.canProceed} onClick={() => void task.continueInvitation('retry')}>继续邀请未发送账号</Button> : null}
              {invitation.blockedCount > 0 ? <Button variant="default" loading={pending === 'reconcile'} disabled={Boolean(pending) || loading} onClick={() => void task.continueInvitation('reconcile')}>核验邀请结果</Button> : null}
            </> : null}
          </>}
        </Group> : null}
        {step === 2 ? <Group gap={12}>
          <TaskConcurrencyControl value={task.taskConcurrency.concurrency} limit={task.taskConcurrency.limit} disabled={!task.taskConcurrency.ready || Boolean(pending) || Boolean(invitation?.activeTaskCount)} onChange={task.taskConcurrency.setConcurrency} />
          <Text size="sm">登录完成 {deliveries?.readyCount ?? 0} / {batch?.targetCount ?? 0}</Text>
          {!task.progress.loggedIn ? <Button loading={pending.startsWith('login:')} disabled={!canJoin} onClick={() => void task.login()}>{invitation?.activeTaskCount ? '正在执行' : batch?.loginStartedAt ? '继续未完成账号' : task.progress.allInvited ? '执行全部 OAuth 登录' : '执行已确认账号 OAuth 登录'}</Button> : <StatusBadge tone="success" label="凭据已就绪" />}
        </Group> : null}
        {cardHeading ? <Group gap={12}><Text size="sm">已生成 {deliveries?.cardCount ?? 0} / {batch?.targetCount ?? 0}</Text>{step === 3 && !task.progress.cardsSaved ? <Button disabled={!editable || !task.progress.canGenerateCards} loading={pending === 'cards'} onClick={() => void task.generateAllCards()}>生成成功账号卡密</Button> : null}<Button variant="default" disabled={Boolean(pending) || loading || !deliveries?.cardCount} loading={pending === 'export'} onClick={() => void task.exportCards()}>导出本轮卡密</Button></Group> : null}
      </Group>
      {blockers.length > 0 && step === 1 ? <Box px={24} py={16}>{blockers.map(item => <Text key={item.code} size="sm" c="warning" role="status">{item.message}</Text>)}{task.joinPreview?.activeBatchId ? <Button variant="subtle" size="sm" mt={8} onClick={() => onOpenOriginal(task.joinPreview!.activeBatchId!)}>查看原操作</Button> : null}</Box> : null}
      {cardHeading && !task.progress.loggedIn ? <Group px={24} py={12} justify="space-between"><Text size="sm" role="status">OAuth 成功 {deliveries?.readyCount ?? 0} · 未完成 {Math.max(0, (batch?.targetCount ?? 0) - (deliveries?.readyCount ?? 0))}</Text><Button variant="subtle" size="sm" onClick={onReturnToOAuth}>返回 OAuth 登录</Button></Group> : null}
      {task.cardProgress && step === 3 ? <Text px={24} py={12} size="sm" role="status">处理 {task.cardProgress.done} / {task.cardProgress.total} · 待确认 {Object.keys(task.rowErrors).length}</Text> : null}
      <Table.ScrollContainer minWidth={cardHeading ? 850 : 650}><Table className="management-table" aria-busy={loading}>
        <Table.Thead><Table.Tr><Table.Th className="account-identity-cell">账号</Table.Th>{step <= 1 ? <><Table.Th>席位类型</Table.Th><Table.Th>邀请状态</Table.Th><Table.Th>处理结果</Table.Th></> : step === 2 ? <><Table.Th>登录结果</Table.Th><Table.Th>处理结果</Table.Th><Table.Th>操作</Table.Th></> : <><Table.Th>登录结果</Table.Th>{cardHeading ? <><Table.Th>卡密</Table.Th><Table.Th>卡密状态</Table.Th></> : <Table.Th>处理结果</Table.Th>}<Table.Th>操作</Table.Th></>}</Table.Tr></Table.Thead>
        <Table.Tbody>{step <= 1 ? (invitation?.targets ?? detail?.targets.map((item) => ({ id: item.id, identifier: item.identifier, targetAccountId: item.id, status: 'not_started', invitationConfirmedAt: undefined, diagnosticCode: task.joinPreview?.availablePremiumSeats === 0 ? 'premium_capacity_exceeded' : undefined })) ?? []).map((item) => <Table.Tr key={item.id}>
          <Table.Td className="account-identity-cell"><AccountIdentity identifier={item.identifier} /></Table.Td>
          <Table.Td><StatusBadge tone="indigo" label={seatTypeLabel(task.joinOperation?.targetSeatType ?? task.joinPreview?.targetSeatType)} /></Table.Td>
          <Table.Td><StatusBadge {...invitationStatus(item)} /></Table.Td>
          <Table.Td>{'stageResults' in item ? <JoinResult target={item} phase="invitation" /> : '—'}</Table.Td>
        </Table.Tr>) : step === 2 ? (invitation?.targets ?? []).map((target) => {
          const running = oauthRunning(target, Boolean(batch?.loginStartedAt));
          return <Table.Tr key={target.id}>
            <Table.Td className="account-identity-cell"><AccountIdentity identifier={target.identifier} /></Table.Td>
            <Table.Td><OAuthResult target={target} started={Boolean(batch?.loginStartedAt)} /></Table.Td>
            <Table.Td>{!target.invitationConfirmedAt ? <JoinResult target={target} phase="invitation" /> : !running ? <OAuthDiagnostic target={target} /> : '—'}</Table.Td>
            <Table.Td>{target.delivery?.status !== 'ready' && !running ? <Button variant="subtle" size="xs" loading={pending === `login:${target.targetAccountId}`} disabled={!canJoin || !target.invitationConfirmedAt} onClick={() => void task.login(target.targetAccountId)}>{!target.invitationConfirmedAt ? '等待邀请' : batch?.loginStartedAt ? '重试登录' : '执行登录'}</Button> : null}</Table.Td>
          </Table.Tr>;
        }) : targets.map((target) => {
          const item = target.delivery;
          const intent = item ? accountCardIntent(item.membershipId) : null; const secret = intent?.saved && item?.cardStatus === 'active' ? intent.secret : null;
          return <Table.Tr key={target.id}><Table.Td className="account-identity-cell"><AccountIdentity identifier={target.identifier} /></Table.Td><Table.Td><OAuthResult target={target} started={Boolean(batch?.loginStartedAt)} diagnostic /></Table.Td>
            <Table.Td><Text size="xs" className="card-secret" title={secret ?? undefined}>{secret ?? (item?.cardDisplaySuffix ? `•••• ${item.cardDisplaySuffix}` : '—')}</Text></Table.Td><Table.Td><StatusBadge tone={item?.cardStatus === 'active' ? 'success' : item?.cardStatus === 'revoked' ? 'error' : 'gray'} label={item?.cardStatus === 'active' ? '已生成' : item?.cardStatus === 'revoked' ? '已撤销' : '未生成'} /></Table.Td>
            <Table.Td><Stack gap={4} align="center">
              {item && (!item.cardStatus || (intent && !intent.saved)) ? <Button variant="subtle" size="xs" disabled={!editable || item.status !== 'ready'} loading={pending === `card:${item.membershipId}`} onClick={() => void task.generateCard(item.membershipId)}>{intent && !intent.saved ? '重试保存' : '生成卡密'}</Button> : null}
              {secret && item ? <Button variant="subtle" size="xs" loading={copier.copyingId === item.membershipId} onClick={() => void copier.copy(item.membershipId, async () => secret)}>{copier.copiedId === item.membershipId ? '已复制' : '复制卡密'}</Button> : null}
              {item && task.rowErrors[item.membershipId] ? <Text size="xs" c="error" role="alert">{task.rowErrors[item.membershipId]}</Text> : null}
            </Stack></Table.Td></Table.Tr>;
        })}
        {!loading && !detail?.targets.length ? <Table.Tr><Table.Td colSpan={columnCount}><Text ta="center" py={32} c="dimmed">本轮暂无账号</Text></Table.Td></Table.Tr> : null}
        {loading && !detail ? <Table.Tr><Table.Td colSpan={columnCount}><Text ta="center" py={32} c="dimmed" role="status">正在读取本轮账号…</Text></Table.Td></Table.Tr> : null}
        </Table.Tbody>
      </Table></Table.ScrollContainer>
      <ListPagination page={task.page} pageSize={task.pageSize} total={invitation?.targetTotal ?? detail?.targetTotal ?? 0} disabled={loading || Boolean(pending)} onPageChange={task.setPage} onPageSizeChange={task.setPageSize} />
    </Paper>
  </Stack>;
}
