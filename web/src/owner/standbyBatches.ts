import createClient from 'openapi-fetch';
import type { components, paths } from '../generated/owner';
import { mutationHeaders, OwnerApiError } from './auth';
import { standbySelectionRequest, standbyWriteSelection } from './standbySelection';

const api = createClient<paths>({ baseUrl: '', credentials: 'include' });
export type Batch = components['schemas']['StandbyChildBatch'];
export type Selection = components['schemas']['StandbyChildSelection'];
export type Scope = components['schemas']['StandbyChildSelectionRequest']['scope'];
export type BatchAccountFilters = Pick<components['schemas']['StandbyChildSelectionRequest'], 'domain' | 'probeStatus' | 'membershipStatus' | 'excludeBatchId' | 'batchId'>;

function result<T>(response: { data?: T; error?: unknown; response: Response }): T {
  if (response.error || response.data === undefined) throw new OwnerApiError(response.response.status, response.error);
  return response.data;
}
export const standbyApi = {
  async list(page = 1, pageSize = 20, search = '', domain = '', signal?: AbortSignal): Promise<components['schemas']['StandbyChildBatchList']> {
    return result(await api.GET('/api/owner/v1/standby-child-batches', {
      ...(signal ? { signal } : {}), params: { query: { page, page_size: pageSize, ...(search ? { search } : {}), ...(domain ? { domain } : {}) } },
    }));
  },
  async get(id: string, signal?: AbortSignal): Promise<Batch> {
    return result(await api.GET('/api/owner/v1/standby-child-batches/{standbyBatchId}', {
      ...(signal ? { signal } : {}), params: { path: { standbyBatchId: id } },
    }));
  },
  async preview(scope: Scope, accountIds: string[], search: string, batchId?: string, filters?: BatchAccountFilters, signal?: AbortSignal): Promise<Selection> {
    const body = { ...standbySelectionRequest(scope, new Set(accountIds), search, batchId), ...(scope === 'filtered' ? filters : {}) };
    return result(await api.POST('/api/owner/v1/standby-child-batches/selection', { ...(signal ? { signal } : {}), params: { header: await mutationHeaders() }, body }));
  },
  async save(name: string, selection: Selection, batch?: Batch, action: 'add' | 'remove' = 'add'): Promise<Batch> {
    const common = { name, selection: standbyWriteSelection(selection), expectedCount: selection.count, confirmed: true };
    if (batch) return result(await api.PATCH('/api/owner/v1/standby-child-batches/{standbyBatchId}', {
      params: { path: { standbyBatchId: batch.id }, header: await mutationHeaders() },
      body: { ...common, expectedVersion: batch.version, action },
    }));
    return result(await api.POST('/api/owner/v1/standby-child-batches', { params: { header: await mutationHeaders() }, body: common }));
  },
  async rename(batch: Batch, name: string): Promise<Batch> {
    return result(await api.PATCH('/api/owner/v1/standby-child-batches/{standbyBatchId}/name', {
      params: { path: { standbyBatchId: batch.id }, header: await mutationHeaders() }, body: { name, expectedVersion: batch.version },
    }));
  },
  async delete(batch: Batch): Promise<void> {
    const response = await api.DELETE('/api/owner/v1/standby-child-batches/{standbyBatchId}', {
      params: { path: { standbyBatchId: batch.id }, header: await mutationHeaders() },
      body: { expectedVersion: batch.version, expectedCount: batch.memberCount, confirmed: true },
    });
    if (!response.response.ok) throw new OwnerApiError(response.response.status, response.error);
  },
  async export(batch: Batch, selection: Selection): Promise<string> {
    return result(await api.POST('/api/owner/v1/standby-child-batches/{standbyBatchId}/export', {
      parseAs: 'text', params: { path: { standbyBatchId: batch.id }, header: await mutationHeaders() },
      body: { selection: standbyWriteSelection(selection), expectedCount: selection.count, expectedVersion: batch.version, confirmed: true },
    }));
  },
};
