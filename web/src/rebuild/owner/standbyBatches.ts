import createClient from 'openapi-fetch';
import type { components, paths } from '../../generated/owner';
import { mutationHeaders, OwnerApiError } from './auth';
import { standbySelectionRequest } from './standbySelection';

const api = createClient<paths>({ baseUrl: '', credentials: 'include' });
export type Batch = components['schemas']['StandbyChildBatch'];
export type Selection = components['schemas']['StandbyChildSelection'];
export type Scope = components['schemas']['StandbyChildSelectionRequest']['scope'];

function result<T>(response: { data?: T; error?: unknown; response: Response }): T {
  if (response.error || response.data === undefined) throw new OwnerApiError(response.response.status, response.error);
  return response.data;
}
export const standbyApi = {
  async list(): Promise<Batch[]> {
    return result(await api.GET('/api/owner/v1/standby-child-batches'));
  },
  async preview(scope: Scope, accountIds: string[], search: string, batchId?: string): Promise<Selection> {
    const body = standbySelectionRequest(scope, new Set(accountIds), search, batchId);
    return result(await api.POST('/api/owner/v1/standby-child-batches/selection', { params: { header: await mutationHeaders() }, body }));
  },
  async save(name: string, selection: Selection, batch?: Batch, action: 'add' | 'remove' = 'add'): Promise<Batch> {
    const common = { name, selection, expectedCount: selection.count, confirmed: true };
    if (batch) return result(await api.PATCH('/api/owner/v1/standby-child-batches/{standbyBatchId}', {
      params: { path: { standbyBatchId: batch.id }, header: await mutationHeaders() },
      body: { ...common, expectedVersion: batch.version, action },
    }));
    return result(await api.POST('/api/owner/v1/standby-child-batches', { params: { header: await mutationHeaders() }, body: common }));
  },
  async export(batch: Batch, selection: Selection): Promise<string> {
    return result(await api.POST('/api/owner/v1/standby-child-batches/{standbyBatchId}/export', {
      params: { path: { standbyBatchId: batch.id }, header: await mutationHeaders() },
      body: { selection, expectedCount: selection.count, expectedVersion: batch.version, confirmed: true },
    }));
  },
};
