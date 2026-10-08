import type { Batch } from './batchOperations';
import type { StatusTone } from '../shared/StatusBadge';

export function operationRecordStatus(batch: Batch): { tone: StatusTone; label: string } {
  if (batch.status === 'ended') return { tone: 'gray', label: '已清退' };
  if (batch.status === 'removing') return { tone: 'indigo', label: '清退中' };
  const facts = batch.execution;
  if (facts.activeTaskCount) return { tone: 'indigo', label: batch.loginStartedAt ? 'OAuth 处理中' : '邀请处理中' };
  if (facts.cardCount === batch.targetCount && batch.targetCount > 0) return { tone: 'success', label: '已完成' };
  if (facts.cardCount) return { tone: 'warning', label: '部分完成' };
  if (facts.invitationConfirmedCount < batch.targetCount) {
    if (facts.invitationWaitingSeatCount) return { tone: 'warning', label: facts.invitationConfirmedCount ? '邀请部分完成' : '等待席位' };
    if (facts.invitationUncertainCount) return { tone: 'warning', label: '邀请待核验' };
    if (facts.invitationFailedCount) return { tone: 'error', label: '邀请未完成' };
    return { tone: 'gray', label: '待发送邀请' };
  }
  if (facts.readyCount === batch.targetCount && batch.targetCount > 0) return { tone: 'success', label: '待生成卡密' };
  if (facts.readyCount) return { tone: 'warning', label: 'OAuth 部分成功' };
  return batch.loginStartedAt ? { tone: 'error', label: 'OAuth 未完成' } : { tone: 'gray', label: '待 OAuth 登录' };
}
