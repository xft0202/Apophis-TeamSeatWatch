import createClient from 'openapi-fetch';
import type { components, paths } from '../generated/owner';
import { mutationHeaders, OwnerApiError } from './auth';

const api = createClient<paths>({ baseUrl: '', credentials: 'include' });
export type OperationStartContext = { requestId: number; motherAccountId: string; workspaceId: string; previousBatchId?: string };
export type WorkspaceDetail = components['schemas']['WorkspaceDetail'];
export type Binding = components['schemas']['Binding'];
export type Batch = components['schemas']['Batch'];
export type BatchDetail = components['schemas']['BatchDetail'];
export type BatchList = components['schemas']['BatchList'];
export type JoinPreview = components['schemas']['JoinPreview'];
export type JoinOperation = components['schemas']['JoinOperation'];
export type Delivery = components['schemas']['Delivery'];
export type DeliveryList = components['schemas']['DeliveryList'];

function result<T>(response: { data?: T; error?: unknown; response: Response }): T {
  if (response.error || response.data === undefined) throw new OwnerApiError(response.response.status, response.error);
  return response.data;
}

export const batchOperationsApi = {
  list: async (bindingId?: string, page = 1, pageSize = 20, signal?: AbortSignal, operationOnly = false, filters: { search?: string; rotation_state?: components['schemas']['BatchRotationState'] } = {}): Promise<BatchList> => result(await api.GET('/api/owner/v1/batches', { ...(signal ? { signal } : {}), params: { query: { ...(bindingId ? { binding_id: bindingId } : {}), page, page_size: pageSize, operation_only: operationOnly, ...filters } } })),
  workspace: async (workspaceId: string, signal?: AbortSignal, pages: { observationPage?: number; observationPageSize?: number; memberPage?: number; memberPageSize?: number } = {}) => result(await api.GET('/api/owner/v1/workspaces/{workspaceId}', { ...(signal ? { signal } : {}), params: { path: { workspaceId }, query: { observation_page: pages.observationPage ?? 1, observation_page_size: pages.observationPageSize ?? 20, member_page: pages.memberPage ?? 1, member_page_size: pages.memberPageSize ?? 20 } } })),
  batch: async (batchId: string, targetPage = 1, targetPageSize = 20, signal?: AbortSignal): Promise<BatchDetail> => result(await api.GET('/api/owner/v1/batches/{batchId}', { ...(signal ? { signal } : {}), params: { path: { batchId }, query: { target_page: targetPage, target_page_size: targetPageSize } } })),
  bind: async (motherAccountId: string, workspaceId: string) => result(await api.POST('/api/owner/v1/bindings', { params: { header: await mutationHeaders() }, body: { motherAccountId, workspaceId } })),
  preview: async (batchId: string, signal?: AbortSignal) => result(await api.GET('/api/owner/v1/batches/{batchId}/join-preview', { ...(signal ? { signal } : {}), params: { path: { batchId } } })),
  startJoin: async (batchId: string, idempotencyKey: string, concurrency = 1) => result(await api.POST('/api/owner/v1/batches/{batchId}/join', { params: { path: { batchId }, header: await mutationHeaders() }, body: { idempotencyKey, confirm: true, concurrency } })),
  reconcileJoin: async (batchId: string, idempotencyKey: string = crypto.randomUUID()) => result(await api.POST('/api/owner/v1/batches/{batchId}/join-reconcile', { params: { path: { batchId }, header: await mutationHeaders() }, body: { idempotencyKey } })),
  retryJoin: async (batchId: string, idempotencyKey: string) => result(await api.POST('/api/owner/v1/batches/{batchId}/join-retry', { params: { path: { batchId }, header: await mutationHeaders() }, body: { idempotencyKey } })),
  operation: async (batchId: string, targetPage = 1, targetPageSize = 20, signal?: AbortSignal) => result(await api.GET('/api/owner/v1/batches/{batchId}/join-operation', { ...(signal ? { signal } : {}), params: { path: { batchId }, query: { target_page: targetPage, target_page_size: targetPageSize } } })),
  deliveries: async (batchId: string, page = 1, pageSize = 20, signal?: AbortSignal) => result(await api.GET('/api/owner/v1/batches/{batchId}/deliveries', { ...(signal ? { signal } : {}), params: { path: { batchId }, query: { page, page_size: pageSize } } })),
  login: async (batchId: string, idempotencyKey: string, targetAccountId?: string, concurrency = 1) => result(await api.POST('/api/owner/v1/batches/{batchId}/deliveries/login', { params: { path: { batchId }, header: await mutationHeaders() }, body: { idempotencyKey, concurrency, ...(targetAccountId ? { targetAccountId } : {}) } })),
};
