import ActionNotice from '../shared/ActionNotice';
import { useActionFeedback } from '../shared/useActionFeedback';
import { Box, Button, Group, Paper, Stack, Text, Title } from '@mantine/core';
import { useEffect, useRef, useState } from 'react';
import { useOperationSelection } from './useOperationSelection';
import { useBatchOperation } from './useBatchOperation';
import { operationSteps, type OperationStep } from './operationWizardState';
import type { OperationStartContext } from './batchOperations';
import OwnerIcon from './OwnerIcon';
import WorkflowSteps from '../shared/WorkflowSteps';
import StatusBadge from '../shared/StatusBadge';
import BatchExecutionView from './BatchExecutionView';
import OperationSelectionView from './OperationSelectionView';
import OperationRecordsView from './OperationRecordsView';
import { formatDateTime, parseDateTimeInput } from '../shared/dateTime';
import { seatTypeLabel } from './seatTypes';

type Props = { active?: boolean; onRepair: (tab: 'workspaces' | 'standby') => void; onOpenCards: (batchId: string) => void; onOpenRotation: (batchId: string) => void; startNextBatch?: OperationStartContext | null };
export default function OperationWizard({ active = true, onRepair, onOpenCards, onOpenRotation, startNextBatch = null }: Props) {
  const [step, setStep] = useState<OperationStep>(0);
  const [view, setView] = useState<'run' | 'records'>('run');
  const [notice, setNotice, dismissNotice] = useActionFeedback('');
  const [historical, setHistorical] = useState(false);
  const task = useBatchOperation(active && view === 'run', setNotice);
  const batch = task.operationBatch;
  const frozen = Boolean(task.joinOperation || historical || (batch && !['planned', 'draft'].includes(batch.status)));
  const selection = useOperationSelection(active && view === 'run' && step === 0 && !frozen, setNotice, task.open);
  const selectionLoading = !frozen && selection.loading;
  const restored = useRef('');
  const nextBatchHandled = useRef(0);
  const busy = Boolean(task.pending) || selection.pending;
  const frontier = selection.dirty && !frozen ? 0 : task.progress.frontier;

  useEffect(() => {
    if (!batch || restored.current === batch.id) return;
    restored.current = batch.id; setStep(task.progress.frontier);
  }, [batch?.id, task.progress.frontier]);
  useEffect(() => {
    if (!active || !startNextBatch || startNextBatch.requestId <= nextBatchHandled.current || busy) return;
    nextBatchHandled.current = startNextBatch.requestId;
    void selection.newOperation(startNextBatch).then((saved) => { if (!saved) return; setHistorical(false); restored.current = ''; setStep(0); setView('run'); });
  }, [active, startNextBatch, busy]);

  async function next() {
    if (busy || task.loading || selectionLoading) return;
    setNotice('');
    if (step === 0 && !frozen) {
      const prepared = await selection.prepare();
      if (!prepared) return;
      restored.current = prepared.id; task.accept(prepared); setStep(1);
    } else if (step < frontier) { setStep((step + 1) as OperationStep); task.setPage(1); }
  }
  async function newOperation() {
    if (busy) return;
    if (!await selection.newOperation()) return; restored.current = ''; setHistorical(false); setView('run'); setStep(0); setNotice('');
  }
  function changeStep(value: number) {
    if (value <= frontier && value !== step) { setStep(value as OperationStep); task.setPage(1); setNotice(''); }
  }
  const motherName = frozen || step > 0 ? batch?.motherAccountName : selection.mother?.loginIdentifier;
  const workspaceName = frozen || step > 0 ? batch?.workspaceName : selection.discovery?.workspaces.find((item) => item.id === selection.draft?.workspaceId)?.displayName;
  const sourceName = frozen || step > 0 ? batch?.sourceBatchName : selection.source?.name;
  const count = frozen || step > 0 ? batch?.targetCount ?? 0 : selection.selected.size;
  const plannedAt = frozen || step > 0 ? batch?.plannedAt : parseDateTimeInput(selection.plannedAt)?.toISOString();
  const complete = [Boolean(batch && (frozen || !selection.dirty)), task.progress.allInvited, task.progress.loggedIn, task.progress.cardsSaved, task.progress.cardsSaved];
  const cannotNext = step === 0 && !frozen ? !selection.workspaceReady || !selection.selected.size || !selection.source || !selection.selection : step >= frontier;
  const nextReason = step === 2 && !task.progress.loggedIn ? `OAuth 登录完成 ${task.deliveries?.readyCount ?? 0} / ${count}` : step === 3 && !task.progress.cardsSaved ? `已生成 ${task.deliveries?.cardCount ?? 0} 张 · OAuth 未完成 ${Math.max(0, count - (task.deliveries?.readyCount ?? 0))}` : '';

  return <section aria-label="开始操作" className="management-page operation-page"><Stack gap={24}>
    <Group className="management-subheader" justify="space-between"><Group gap={12}><Box className="management-page-icon"><OwnerIcon name="operation" size={20} /></Box><Title order={2}>开始操作</Title></Group><Group gap={8}>
      <Button variant="default" disabled={busy} onClick={() => { setView(view === 'records' ? 'run' : 'records'); setNotice(''); }}>{view === 'records' ? '返回本轮操作' : '操作记录'}</Button>
      <Button variant="default" disabled={busy} onClick={() => void newOperation()}>新建操作</Button>
      {view === 'run' ? <Button variant="default" disabled={busy || task.loading || selectionLoading} leftSection={<OwnerIcon name="refresh" size={16} />} onClick={() => { setNotice(''); if (step === 0 && !frozen) selection.reload(); else task.refresh(); }}>刷新</Button> : null}
    </Group></Group>
    <ActionNotice message={notice} onClose={dismissNotice} />
    {view === 'records' ? <OperationRecordsView active={active} onOpen={(id) => { restored.current = ''; setHistorical(id !== selection.draft?.executionBatchId); task.open(id); setStep(1); setView('run'); setNotice(''); }} /> : <>
      <WorkflowSteps steps={operationSteps.map((label, index) => ({ label, complete: Boolean(complete[index]), available: index <= frontier, status: complete[index] ? '已完成' : (index === 1 && task.progress.invited && !task.progress.allInvited) || index === 2 && task.progress.canGenerateCards ? '部分成功' : (index === 3 || index === 4) && Boolean(task.deliveries?.cardCount) ? '部分完成' : (index === 1 && !task.progress.invited && Boolean(task.joinOperation?.activeTaskCount)) || (index === 2 && Boolean(batch?.loginStartedAt) && Boolean(task.joinOperation?.activeTaskCount)) || (index === 3 && task.pending === 'cards') ? '进行中' : index === frontier && index > 0 ? nextReason && step === index ? '待处理' : '待完成' : index === 0 ? '待选择' : '待开始' }))} current={step} onChange={changeStep} />
      <Paper withBorder radius={12} className="operation-context"><Box className="operation-context-item"><Text size="xs" c="dimmed">母号</Text><Text size="sm" fw={600} title={motherName}>{motherName ?? '待选择'}</Text></Box><Box className="operation-context-item"><Text size="xs" c="dimmed">目标空间</Text><Group gap={8}><Text size="sm" fw={600} title={workspaceName}>{workspaceName ?? '待选择'}</Text><StatusBadge tone={task.joinOperation && !task.joinOperation.targetSeatType ? 'warning' : 'indigo'} label={seatTypeLabel(task.joinOperation ? task.joinOperation.targetSeatType : 'prolite')} /></Group></Box><Box className="operation-context-item"><Text size="xs" c="dimmed">来源批次</Text><Text size="sm" fw={600} title={sourceName}>{sourceName ?? '待选择'}{sourceName ? ` · ${count} 个账号` : ''}</Text></Box><Box className="operation-context-item"><Text size="xs" c="dimmed">计划清退</Text><Text size="sm" fw={600}>{plannedAt ? formatDateTime(plannedAt) : '待设置'}</Text></Box></Paper>
      {step === 0 && !frozen ? <OperationSelectionView selection={selection} onRepair={onRepair} /> : <>
        {step === 0 && frozen ? <Group gap={12}><StatusBadge tone="gray" label="本轮范围已锁定" /></Group> : null}
        {step === 4 ? <Group className="operation-outcome" justify="space-between"><Group gap={16}><StatusBadge tone={task.progress.cardsSaved ? "success" : "warning"} label={task.progress.cardsSaved ? "本轮已完成" : "部分完成"} /><Text size="sm">加入 {task.joinOperation?.succeededCount ?? 0} · 凭据 {task.deliveries?.readyCount ?? 0} · 卡密 {task.deliveries?.cardCount ?? 0}</Text></Group><Group gap={8}><Button disabled={!batch} onClick={() => { if (batch) onOpenCards(batch.id); }}>查看本轮卡密</Button><Button variant="default" disabled={!batch} onClick={() => { if (batch) onOpenRotation(batch.id); }}>查看本轮轮转</Button></Group></Group> : null}
        <BatchExecutionView task={task} step={step} onReturnToOAuth={() => changeStep(2)} onOpenOriginal={(id) => { restored.current=''; setHistorical(true); task.open(id); setStep(1); setNotice(''); }} />
      </>}
      <Group className="operation-footer" justify="space-between"><Group gap={12}><Button variant="default" disabled={step === 0} onClick={() => changeStep(step - 1)}>上一步</Button><Text size="sm" c="dimmed">第 {step + 1} / 5 步</Text></Group><Group gap={12}>{nextReason ? <Text size="sm" c="dimmed" role="status">{nextReason}</Text> : null}{step < 4 ? <Button variant={step === 0 && !frozen ? 'filled' : 'default'} disabled={busy || task.loading || selectionLoading || cannotNext} loading={step === 0 && selection.pending} onClick={() => void next()}>{step === 0 && !frozen ? '核对并下一步' : step === 1 && task.progress.invited ? '继续 OAuth 登录' : '下一步'}</Button> : null}</Group></Group>
    </>}
  </Stack></section>;
}
