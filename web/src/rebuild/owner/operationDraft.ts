import createClient from 'openapi-fetch';
import type { components, paths } from '../../generated/owner';
import { mutationHeaders, OwnerApiError } from './auth';

const api = createClient<paths>({ baseUrl: '', credentials: 'include' });
export type Draft = components['schemas']['OperationDraft'];
export type DraftChange = components['schemas']['OperationDraftChange'];
export type DraftChild = components['schemas']['OperationDraftChild'];

function result<T>(response: { data?: T; error?: unknown; response: Response }): T {
  if (response.error || response.data === undefined) throw new OwnerApiError(response.response.status, response.error);
  return response.data;
}
export const operationDraftApi = {
  get: async () => result(await api.GET('/api/owner/v1/operation-draft')),
  batchChildren: async (standbyBatchId: string, page: number) => result(await api.GET('/api/owner/v1/operation-draft/batches/{standbyBatchId}/children', {
    params: { path: { standbyBatchId }, query: { page } },
  })),
  start: async () => result(await api.POST('/api/owner/v1/operation-draft', { params: { header: await mutationHeaders() } })),
  change: async (body: DraftChange) => result(await api.PATCH('/api/owner/v1/operation-draft', { params: { header: await mutationHeaders() }, body })),
};
