import type { components } from '../generated/owner';

const labels: Record<string, string> = { default: '普通席位', prolite: '高级席位', usage_based: '按量席位', automation: '自动化席位' };
export function seatTypeLabel(value?: string): string { return value ? labels[value] ?? '未确认' : '未确认'; }

export function workspaceSeatSummary(facts: components['schemas']['SelectedWorkspaceVerification'] | null) {
  const members = facts?.seatTypeCounts;
  const invites = facts?.pendingInviteSeatTypeCounts;
  const entitlements = facts?.seatEntitlements;
  const types = ['default', 'prolite', ...['usage_based', 'automation', 'unknown'].filter((type) => (members?.[type] ?? 0) + (invites?.[type] ?? 0) > 0)];
  return types.map((type) => {
    const opened = entitlements?.[type];
    const occupied = members?.[type];
    const invitations = invites ? invites[type] ?? 0 : undefined;
    const remaining = opened !== undefined && occupied !== undefined && invitations !== undefined ? Math.max(0, opened - occupied - invitations) : undefined;
    return { type, label: seatTypeLabel(type), opened, members: occupied, invitations, remaining };
  });
}
