import type { ExpiryAssignment, ExpiryPreview } from './expiryRotation';

export function explicitRotationAssignments(item: ExpiryPreview, chosen: Record<string, string>): ExpiryAssignment[] | null {
  const slots = item.slots.filter((slot) => slot.decision === 'replaceable');
  const eligible = item.candidates.filter((candidate) => candidate.decision === 'eligible');
  if (!slots.length || eligible.length !== slots.length || Object.keys(chosen).length !== slots.length) return null;
  const unique = new Set<string>();
  const mapped: ExpiryAssignment[] = [];
  for (const slot of slots) {
    const candidate = eligible.find((child) => child.accountId === chosen[slot.platformMemberId]);
    if (!candidate || candidate.seatType !== slot.seatType || unique.has(candidate.accountId)) return null;
    unique.add(candidate.accountId);
    mapped.push({ platformMemberId: slot.platformMemberId, accountId: candidate.accountId });
  }
  return mapped;
}

export function canConfirmRotation(item: ExpiryPreview, draftVersion: number, now: number): boolean {
  return item.status === 'ready' && !item.authorized && item.draftVersion === draftVersion &&
    new Date(item.expiresAt).getTime() > now && item.slots.some((slot) => slot.decision === 'replaceable') &&
    item.candidates.filter((candidate) => candidate.decision === 'eligible').length === item.slots.filter((slot) => slot.decision === 'replaceable').length;
}
export function rotationCandidateStatus(candidate: ExpiryPreview['candidates'][number]): string {
  if (candidate.deliveryStatus === 'join_candidate_pending_first_probe') {
    return '仅可作为受控加入候选；加入后须实测目标空间零用量并成功落账，当前不可交付。';
  }
  if (candidate.reason === 'usage_absence_unverified' || candidate.reason === 'usage_unknown') {
    return '未取得完整的加入前无首次用量记录证明；探测失败或未知不能当作无记录。';
  }
  if (candidate.reason === 'sticky_usage_conflict' || candidate.reason === 'sticky_usage_protected') {
    return '曾有用量记录，不可按未使用新号加入或交付。';
  }
  return `不可加入或交付：${candidate.reason}`;
}

export function rotationStatus(item: ExpiryPreview): string {
  switch (item.status) {
    case 'pending_permission': return '写入管理权限缺少独立证据；可读取空间不代表允许管理。本预览不能授权。';
    case 'facts_incomplete': return '订阅、席位类型、完整名单或保护事实不全/已过期；请重新核验。';
    case 'not_expired': return '空间订阅尚未到期；不能授权到期换批。';
    case 'needs_verification': return '原席位保护、候选资格或席位与合格候选数待核验；请修订草案后重新预览。不会整批清退，也不会临时发邀请。';
    case 'authorized': return '已记录冻结授权；本阶段不执行邀请、加入、清退或推送。';
    case 'revoked': return '授权已撤销；原确认不能再次使用。';
    case 'ready': return '只读预览仅核对逐席受控加入候选；加入后首次目标空间零用量实测与持久化前不可交付。授权写入围栏待实现，本阶段不会建立可执行授权。';
  }
}
