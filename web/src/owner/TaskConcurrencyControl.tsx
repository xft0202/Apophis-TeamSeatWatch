import { Group, NumberInput, Text } from '@mantine/core';
import { useId } from 'react';

type Props = { value: number; limit: number; disabled?: boolean; onChange: (value: number | string) => void };
export default function TaskConcurrencyControl({ value, limit, disabled = false, onChange }: Props) {
	const id = useId();
	return <Group className="task-concurrency-control" gap={8} wrap="nowrap">
		<Text component="label" htmlFor={id} size="sm">任务并发</Text>
		<NumberInput id={id} aria-label="本次任务并发" value={value} min={1} max={limit} allowDecimal={false} disabled={disabled} onChange={onChange} w={80} size="xs" />
	</Group>;
}
