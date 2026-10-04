import type { ExpiryAssignment, ExpiryPreview } from './expiryRotation';

export function explicitRotationAssignments(item: ExpiryPreview, chosen: Record<string, string>): ExpiryAssignment[] | null {
  const slots = item.slots.filter((slot) => slot.decision === 'replaceable');
  const eligible = item.candidates.filter((candidate) => candidate.decision === 'eligible');
  if (!slots.length || eligible.length !== slots.length || eligible.some((candidate) => candidate.deliveryStatus !== 'join_candidate_pending_first_probe') || Object.keys(chosen).length !== slots.length) return null;
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
  const eligible = item.candidates.filter((candidate) => candidate.decision === 'eligible');
  return item.status === 'ready' && !item.authorized && item.draftVersion === draftVersion &&
    new Date(item.expiresAt).getTime() > now && item.slots.some((slot) => slot.decision === 'replaceable') &&
    eligible.length === item.slots.filter((slot) => slot.decision === 'replaceable').length &&
    eligible.every((candidate) => candidate.deliveryStatus === 'join_candidate_pending_first_probe');
}
export function rotationCandidateStatus(candidate: ExpiryPreview['candidates'][number]): string {
  if (candidate.protectionStatus === 'delivered' || candidate.protectionStatus === 'canceled_retired' || candidate.reason === 'global_delivery_protected') {
    return '已全局保护 · 不可解除';
  }
  if (candidate.deliveryStatus === 'join_candidate_pending_first_probe') {
    return '待加入 · 当前不可交付';
  }
  if (candidate.reason === 'legacy_preview_requires_repreview') {
    return '需重新预览核验';
  }
  if (candidate.reason === 'usage_absence_unverified' || candidate.reason === 'usage_unknown') {
    return '用量记录待核验';
  }
  if (candidate.reason === 'sticky_usage_conflict' || candidate.reason === 'sticky_usage_protected') {
    return '曾有用量 · 不可加入';
  }
  return candidate.reason === 'invitation_required' ? '需邀请' : '资格待核验';
}

export function rotationStatus(item: ExpiryPreview): string {
  switch (item.status) {
    case 'pending_permission': return '管理权限待核验';
    case 'facts_incomplete': return '空间事实待核验';
    case 'not_expired': return '空间订阅尚未到期';
    case 'needs_verification': return '席位或候选待核验';
    case 'authorized': return '已确认 · 待执行';
    case 'revoked': return '授权已撤销';
    case 'ready': return '预览已核验 · 待确认';
    default: return '状态待核验';
  }
}
