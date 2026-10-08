import { Text } from '@mantine/core';
import { formatDateTime } from './dateTime';

export default function Timestamp({ value }: { value: string | undefined }) {
  return <Text component="span" size="xs" className="date-time">{value ? formatDateTime(value, false) : '—'}</Text>;
}
