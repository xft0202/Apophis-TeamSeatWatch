import type { Removal, RemovalSlot } from './rotationRemoval';

export function removalSlotStatus(slot: RemovalSlot): string {
  if (slot.state === 'absent_verified') return slot.candidateReady ? '空位已核实' : '空位证据待更新';
  return {
    pending: '待清退', lease_acquired: '正在预检', remove_requested: '清退请求已发出',
    remote_result_uncertain: '原席待核验', absent_verification_pending: '正在核实空位',
    blocked: '预检未通过', stopped: '已停止',
  }[slot.state];
}
export function removalSlotAction(removal: Removal, slot: RemovalSlot, now: number): 'run' | 'verify' | null {
  if (slot.leaseExpiresAt && Date.parse(slot.leaseExpiresAt) > now) return null;
  // Reentry and inactive authorization reconciliation are read-only. A receipt
  // or even a complete historical absence never grants another DELETE.
  if (slot.remoteRequestId) return 'verify';
  if (!removal.writeAllowed || removal.stopped || slot.state === 'stopped') return null;
  return slot.state === 'pending' || slot.state === 'blocked' || slot.state === 'lease_acquired' ? 'run' : null;
}
export function removalErrorMessage(status?: number): string {
  if (status === 409) return '原授权、席位事实或执行租约已变化。已刷新进度；不要重建目标或重发清退。';
  if (status === 401 || status === 403) return '登录或管理权限已失效，请重新登录后核实原槽。';
  return '操作未完成，原席仍按占用或待核验保留。请刷新进度后核实原槽，不要重复清退。';
}
