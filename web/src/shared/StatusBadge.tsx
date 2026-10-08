import { Badge } from '@mantine/core';

export type StatusTone = 'gray' | 'indigo' | 'success' | 'warning' | 'error';

export default function StatusBadge({ label, tone }: { label: string; tone: StatusTone }) {
  return <Badge color={tone} variant="light">{label}</Badge>;
}
