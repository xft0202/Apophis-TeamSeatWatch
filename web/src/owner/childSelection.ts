export type ChildScope = 'selected' | 'filtered';

export function togglePage(selected: ReadonlySet<string>, pageIds: string[]): Set<string> {
  const next = new Set(selected);
  if (pageIds.every((id) => next.has(id))) pageIds.forEach((id) => next.delete(id));
  else pageIds.forEach((id) => next.add(id));
  return next;
}

export function exportRange(scope: ChildScope, selected: ReadonlySet<string>, search: string, total: number) {
  return scope === 'selected'
    ? { scope, accountIds: [...selected].sort(), search: '', count: selected.size }
    : { scope, accountIds: [], search, count: total };
}
