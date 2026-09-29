export type StandbyScope = 'selected' | 'filtered' | 'batch';

// A preview always receives one explicit scope, never the currently visible page.
export async function frozenStandbyPreview<T>(request: () => Promise<T>, generation: number, currentGeneration: () => number): Promise<T | null> {
  const selection = await request();
  return currentGeneration() === generation ? selection : null;
}

export function standbySelectionRequest(scope: StandbyScope, selected: ReadonlySet<string>, search: string, batchId?: string) {
  if (scope === 'selected') return { scope, accountIds: [...selected].sort() };
  if (scope === 'filtered') return { scope, search };
  if (!batchId) throw new Error('batch id required');
  return { scope, batchId };
}
