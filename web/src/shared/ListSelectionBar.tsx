import { Button, Group, Text } from '@mantine/core';
import type { ReactNode } from 'react';

type Props = { count: number; total: number; disabled?: boolean; selecting?: boolean; onSelectAll?: () => void; onClear?: () => void; actions?: ReactNode; noun?: string; unit?: string; allSelected?: boolean };
export default function ListSelectionBar({ count, total, disabled = false, selecting = false, onSelectAll, onClear, actions, noun = '账号', unit = '个', allSelected = count === total }: Props) {
  if (count === 0) return null;
  return <Group className="account-selection-bar" justify="space-between"><Group gap={12}><Text size="sm">已选 {count} {unit}{noun}</Text>{onSelectAll && total > 0 && !allSelected ? <Button variant="subtle" size="xs" loading={selecting} disabled={disabled || total > 10000} title={total > 10000 ? `最多选择 10000 ${unit}${noun}` : undefined} onClick={onSelectAll}>选择全部筛选结果 {total} {unit}</Button> : null}{onClear ? <Button variant="subtle" color="gray" size="xs" disabled={disabled || selecting} onClick={onClear}>取消选择</Button> : null}</Group>{actions}</Group>;
}
