export type StandbyScope = 'selected' | 'filtered' | 'batch';

export function standbyRangeLimitNotice(actualCount: number | undefined, operation: 'preview' | 'save'): string {
  const size = actualCount === undefined ? '超过 10000 个账号' : `${actualCount} 个账号`;
  return operation === 'preview'
    ? `当前范围实际有 ${size}，单次冻结最多 10000 个；请缩小筛选条件或减少勾选，不能只处理前 10000 个。`
    : `加入后批次将有 ${size}，超过硬上限 10000；未移动任何账号。请减少勾选或先移出批次成员，再重试。`;
}

export function standbySelectionRequest(scope: StandbyScope, selected: ReadonlySet<string>, search: string, batchId?: string) {
  if (scope === 'selected') return { scope, accountIds: [...selected].sort() };
  if (scope === 'filtered') return { scope, search };
  if (!batchId) throw new Error('batch id required');
  return { scope, batchId };
}

// Source names are display metadata; writes carry only the frozen identity and
// membership version checked by the server.
export function standbyWriteSelection<T extends { scope: StandbyScope; count: number; members: { accountId: string; membershipVersion: number }[] }>(selection: T) {
  return { scope: selection.scope, count: selection.count, members: selection.members.map(({ accountId, membershipVersion }) => ({ accountId, membershipVersion })) };
}
