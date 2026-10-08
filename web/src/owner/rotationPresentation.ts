import type { RotationState } from './batchRotation';
import type { StatusTone } from '../shared/StatusBadge';
import { formatDateTime } from '../shared/dateTime';

type Label = { tone: StatusTone; label: string };
export const rotationLabels: Record<RotationState, Label> = {
  not_started: { tone: 'gray', label: '未开始' }, joining: { tone: 'indigo', label: '邀请中' },
  not_due: { tone: 'gray', label: '未到期' }, pending_removal: { tone: 'warning', label: '待清退' },
  removing: { tone: 'indigo', label: '清退中' }, needs_attention: { tone: 'error', label: '需处理' },
  pending_check: { tone: 'warning', label: '待核验' }, completed: { tone: 'success', label: '已完成' },
};
export const memberLabels: Record<string, Label> = {
  present: { tone: 'success', label: '仍在空间' }, absent: { tone: 'warning', label: '未确认在空间' },
  ambiguous: { tone: 'warning', label: '身份待核验' }, protected_owner: { tone: 'warning', label: '空间所有者' },
  removed: { tone: 'gray', label: '已退出空间' },
};
export const removalLabels: Record<string, Label> = {
  queued: { tone: 'gray', label: '等待处理' }, running: { tone: 'indigo', label: '处理中' },
  succeeded: { tone: 'success', label: '已清退' }, unknown: { tone: 'warning', label: '待核验' },
  failed: { tone: 'error', label: '失败' }, blocked: { tone: 'error', label: '需处理' },
};
export function removalReason(code?: string): string {
  if (!code) return '—';
  if (/owner|protected/.test(code)) return '空间所有者受保护';
  if (/identity|ambiguous|member_id/.test(code)) return '成员身份需要核对';
  if (/snapshot|evidence/.test(code)) return '空间成员信息需要更新';
  if (/credential|token|permission|auth/.test(code)) return '空间权限或登录需要核对';
  if (/uncertain|unknown|timeout|receipt/.test(code)) return '清退结果需要核验';
  if (/absent|removed|succeeded/.test(code)) return '已确认退出空间';
  return '请核验原成员的清退结果';
}
export function rotationTime(value?: string) { return value ? formatDateTime(value) : '—'; }
