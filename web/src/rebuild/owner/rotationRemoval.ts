import createClient from 'openapi-fetch';
import type { components, paths } from '../../generated/owner';
import { mutationHeaders, OwnerApiError } from './auth';
import type { ExpiryPreview } from './expiryRotation';

const api = createClient<paths>({ baseUrl: '', credentials: 'include' });
export type Removal = components['schemas']['RotationRemoval'];
export type RemovalSlot = components['schemas']['RotationRemovalSlot'];
export type RemovalHistory = components['schemas']['RotationRemovalHistory'];
function result<T>(response: { data?: T; error?: unknown; response: Response }): T {
  if (response.error || response.data === undefined) throw new OwnerApiError(response.response.status, response.error);
  return response.data;
}
export const rotationRemovalApi = {
  history: async (page = 1) => result(await api.GET('/api/owner/v1/expiry-rotation/removals', { params: { query: { page } } })),
  get: async (previewId: string) => result(await api.GET('/api/owner/v1/expiry-rotation/previews/{previewId}/removal', { params: { path: { previewId } } })),
  start: async (preview: ExpiryPreview) => {
    const digest = preview.authorizationDigest;
    if (!digest) throw new OwnerApiError(422, undefined);
    return result(await api.POST('/api/owner/v1/expiry-rotation/previews/{previewId}/removal', {
      params: { path: { previewId: preview.id }, header: await mutationHeaders() },
      // Stable for this immutable authorization after refresh, lost response or
      // a new login. No target reconstruction and no second authorization.
      body: { confirmed: true, authorizationDigest: digest, idempotencyKey: preview.id },
    }));
  },
  run: async (previewId: string, slotId: string) => result(await api.POST('/api/owner/v1/expiry-rotation/previews/{previewId}/removal/slots/{slotId}/run', {
    params: { path: { previewId, slotId }, header: await mutationHeaders() }, body: { confirmed: true },
  })),
  verify: async (previewId: string, slotId: string) => result(await api.POST('/api/owner/v1/expiry-rotation/previews/{previewId}/removal/slots/{slotId}/verify', {
    params: { path: { previewId, slotId }, header: await mutationHeaders() }, body: { confirmed: true },
  })),
  stop: async (previewId: string) => result(await api.POST('/api/owner/v1/expiry-rotation/previews/{previewId}/removal/stop', {
    params: { path: { previewId }, header: await mutationHeaders() }, body: { confirmed: true },
  })),
};
