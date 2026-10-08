import createClient from 'openapi-fetch';
import type { components, paths } from '../generated/owner';
import { mutationHeaders, OwnerApiError } from './auth';

const api = createClient<paths>({ baseUrl: '', credentials: 'include' });
export type Draft = components['schemas']['OperationDraft'];
export type DraftChange = components['schemas']['OperationDraftChange'];

function result<T>(response: { data?: T; error?: unknown; response: Response }): T {
  if (response.error || response.data === undefined) throw new OwnerApiError(response.response.status, response.error);
  return response.data;
}
export const operationDraftApi = {
  get: async (signal?: AbortSignal) => result(await api.GET('/api/owner/v1/operation-draft', { ...(signal ? { signal } : {}) })),
  start: async () => result(await api.POST('/api/owner/v1/operation-draft', { params: { header: await mutationHeaders() } })),
  reset: async (previousBatchId?: string) => result(await api.DELETE('/api/owner/v1/operation-draft', { params: { header: await mutationHeaders(), query: { ...(previousBatchId ? { previous_batch_id: previousBatchId } : {}) } } })),
  change: async (body: DraftChange) => result(await api.PATCH('/api/owner/v1/operation-draft', { params: { header: await mutationHeaders() }, body })),
  prepare: async (body: components['schemas']['PrepareOperationDraft']) => result(await api.POST('/api/owner/v1/operation-draft/prepare', { params: { header: await mutationHeaders() }, body })),
};
