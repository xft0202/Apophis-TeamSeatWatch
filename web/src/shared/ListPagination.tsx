import { Group, Pagination, Select, Text } from '@mantine/core';

type Props = {
  page: number;
  pageSize: number;
  total: number;
  disabled?: boolean;
  showPageSize?: boolean;
  onPageChange: (page: number) => void;
  onPageSizeChange: (pageSize: number) => void;
};

export default function ListPagination({ page, pageSize, total, disabled = false, showPageSize = true, onPageChange, onPageSizeChange }: Props) {
  return <Group className="list-pagination" justify="space-between">
    <Text size="sm" c="dimmed">{total === 0 ? '0' : `${(page - 1) * pageSize + 1}–${Math.min(page * pageSize, total)}`} / {total} 条</Text>
    <Group gap={16}>
      {showPageSize ? <Select className="list-page-size" aria-label="每页条数" value={String(pageSize)} data={['20', '50', '100'].map((value) => ({ value, label: `${value} 条 / 页` }))} allowDeselect={false} disabled={disabled} onChange={(value) => { if (value) { onPageSizeChange(Number(value)); } }} /> : null}
      <Pagination disabled={disabled} total={Math.max(1, Math.ceil(total / pageSize))} value={page} onChange={onPageChange} />
    </Group>
  </Group>;
}
