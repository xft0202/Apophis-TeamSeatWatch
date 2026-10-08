import { Group, Stack, Text } from '@mantine/core';
import StatusBadge from '../shared/StatusBadge';
import { formatDateTime } from '../shared/dateTime';
import { useSelectedWorkspace } from './useSelectedWorkspace';
import { canShowSelectedWorkspaceReadSource, workspaceSubscriptionState } from './workspaceVerification';
import WorkspaceSeatTable from './WorkspaceSeatTable';
import type { components } from '../generated/owner';

export default function WorkspaceSubscriptionNotice({ motherAccountId, workspaceId, preview }: { motherAccountId: string; workspaceId: string; preview: components['schemas']['JoinPreview'] | null }) {
  const state = useSelectedWorkspace(motherAccountId, workspaceId);
  const subscription = workspaceSubscriptionState(state.facts, state.access, state.now);
  if (state.loading) return <Text size="sm" c="dimmed" role="status">正在读取订阅状态</Text>;
  const content = <Group gap={12}><Text size="sm">当前空间订阅</Text><StatusBadge {...subscription} />{subscription.observedAt ? <Text size="xs" c="dimmed">账单同步：{formatDateTime(subscription.observedAt)}</Text> : null}</Group>;
  const premiumCapacity = preview?.batch.workspaceId === workspaceId && preview.batch.motherAccountId === motherAccountId && preview.paidPremiumSeats !== undefined && preview.occupiedPremiumSeats !== undefined && preview.reservedPremiumSeats !== undefined && preview.availablePremiumSeats !== undefined
    ? { opened: preview.paidPremiumSeats, members: preview.occupiedPremiumSeats, invitations: preview.reservedPremiumSeats, remaining: preview.availablePremiumSeats } : undefined;
  return <Stack gap={12}><WorkspaceSeatTable facts={state.facts} subscriptionCurrent={canShowSelectedWorkspaceReadSource(state.facts, state.access, 'subscriptions', state.now)} countsCurrent={canShowSelectedWorkspaceReadSource(state.facts, state.access, 'seat_type_counts', state.now)} rosterCurrent={state.ready} premiumCapacity={premiumCapacity} />{content}</Stack>;
}
