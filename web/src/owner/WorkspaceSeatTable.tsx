import { Paper, Table } from '@mantine/core';
import type { components } from '../generated/owner';
import StatusBadge from '../shared/StatusBadge';
import { workspaceSeatSummary } from './seatTypes';

export type InvitationSeatCapacity = { opened: number; members: number; invitations: number; remaining: number };

export default function WorkspaceSeatTable({ facts, subscriptionCurrent, countsCurrent, rosterCurrent, premiumCapacity }: { facts: components['schemas']['SelectedWorkspaceVerification'] | null; subscriptionCurrent: boolean; countsCurrent: boolean; rosterCurrent: boolean; premiumCapacity?: InvitationSeatCapacity | undefined }) {
  const rows = workspaceSeatSummary(facts).map(item => item.type === 'prolite' && premiumCapacity ? { ...item, ...premiumCapacity } : item);
  return <Paper withBorder radius={8} className="management-list-panel"><Table.ScrollContainer minWidth={600}><Table className="management-table" aria-label="席位分类"><Table.Thead><Table.Tr><Table.Th>席位类型</Table.Th><Table.Th>已开通席位</Table.Th><Table.Th>已占用成员</Table.Th><Table.Th>{premiumCapacity ? '待接受／处理中' : '待接受账号'}</Table.Th><Table.Th>剩余可邀请</Table.Th></Table.Tr></Table.Thead><Table.Tbody>{rows.map(item => <Table.Tr key={item.type}>
    <Table.Td><StatusBadge tone={item.type === 'prolite' ? 'indigo' : item.type === 'unknown' ? 'warning' : 'gray'} label={item.label} /></Table.Td>
    <Table.Td>{subscriptionCurrent ? item.opened ?? '未获取' : '未同步'}</Table.Td><Table.Td>{countsCurrent ? item.members ?? '未获取' : '未同步'}</Table.Td><Table.Td>{rosterCurrent ? item.invitations ?? '未获取' : '未同步'}</Table.Td>
    <Table.Td>{subscriptionCurrent && countsCurrent && rosterCurrent ? item.remaining === undefined ? '未获取' : <StatusBadge tone={item.remaining > 0 ? 'success' : 'warning'} label={String(item.remaining)} /> : '未同步'}</Table.Td>
  </Table.Tr>)}</Table.Tbody></Table></Table.ScrollContainer></Paper>;
}
