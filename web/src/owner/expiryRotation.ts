import createClient from 'openapi-fetch';
import type { components, paths } from '../generated/owner';
import { mutationHeaders, OwnerApiError } from './auth';

const api = createClient<paths>({ baseUrl: '', credentials: 'include' });
export type ExpiryPreview = components['schemas']['ExpiryRotationPreview'];
export type ExpiryAssignment = components['schemas']['ExpiryRotationAssignment'];
function result<T>(response: { data?: T; error?: unknown; response: Response }): T {
  if (response.error || response.data === undefined) throw new OwnerApiError(response.response.status, response.error);
  return response.data;
}
export const expiryRotationApi = {
  get: async (previewId: string) => result(await api.GET('/api/owner/v1/expiry-rotation/previews/{previewId}', { params: { path: { previewId } } })),
  latest: async () => result(await api.GET('/api/owner/v1/expiry-rotation/previews/latest')),
  preview: async () => result(await api.POST('/api/owner/v1/expiry-rotation/previews', { params: { header: await mutationHeaders() } })),
  confirm: async (item: ExpiryPreview, idempotencyKey: string, assignments: ExpiryAssignment[]) => result(await api.POST('/api/owner/v1/expiry-rotation/previews/{previewId}/confirm', {
    params: { path: { previewId: item.id }, header: await mutationHeaders() },
    body: { confirmed: true, digest: item.digest, idempotencyKey, assignments },
  })),
  revoke: async (item: ExpiryPreview) => result(await api.POST('/api/owner/v1/expiry-rotation/previews/{previewId}/revoke', {
    params: { path: { previewId: item.id }, header: await mutationHeaders() },
  })),
};
