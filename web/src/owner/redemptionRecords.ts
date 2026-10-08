import createClient from 'openapi-fetch';
import type { components, paths } from '../generated/owner';
import type { StatusTone } from '../shared/StatusBadge';
import { mutationHeaders, OwnerApiError } from './auth';

const api = createClient<paths>({ baseUrl: '', credentials: 'include' });
export type RedemptionRecord = components['schemas']['RedemptionRecord'];
export type RedemptionDetail = components['schemas']['RedemptionRecordDetail'];
export type RedemptionList = components['schemas']['RedemptionRecordList'];
export type CredentialState = components['schemas']['RedemptionCredentialState'];
export type ReclaimState = components['schemas']['RedemptionReclaimState'];
export type RedemptionQuery = paths['/api/owner/v1/redemptions']['get']['parameters']['query'];
export type RedemptionScope = { mother_account_id: string; workspace_id: string };
export type RedemptionFocus = { membershipId: string; batchId: string; motherAccountId: string; workspaceId: string; requestId: number };
function result<T>(response: { data?: T; error?: unknown; response: Response }): T {
  if (response.error || response.data === undefined) throw new OwnerApiError(response.response.status, response.error);
  return response.data;
}
export const redemptionRecordsApi = {
  list: async (query: RedemptionQuery, signal: AbortSignal) => result(await api.GET('/api/owner/v1/redemptions', { signal, params: { query } })),
  batches: async (scope: RedemptionScope, search: string, page: number, signal: AbortSignal) => result(await api.GET('/api/owner/v1/redemptions/batches', { signal, params: { query: { ...scope, page, page_size: 20, ...(search ? { search } : {}) } } })),
  detail: async (membershipId: string, query: paths['/api/owner/v1/redemptions/{membershipId}']['get']['parameters']['query'], signal: AbortSignal) => result(await api.GET('/api/owner/v1/redemptions/{membershipId}', { signal, params: { path: { membershipId }, query } })),
  authorize: async (membershipId: string, idempotencyKey: string) => result(await api.POST('/api/owner/v1/deliveries/{membershipId}/reclaim', { params: { path: { membershipId }, header: await mutationHeaders() }, body: { idempotencyKey } })),
};
type StateLabel = { label: string; tone: StatusTone };
export const credentialLabels: Record<CredentialState, StateLabel> = {
  healthy: { label: '凭据正常', tone: 'success' }, needs_reclaim: { label: '需要找回', tone: 'warning' },
  reclaiming: { label: '找回中', tone: 'indigo' }, unavailable: { label: '不可用', tone: 'error' },
  pending_check: { label: '待核验', tone: 'warning' }, revoked: { label: '卡密已撤销', tone: 'error' }, ended: { label: '服务已结束', tone: 'gray' },
};
export const reclaimLabels: Record<ReclaimState, StateLabel> = {
  not_requested: { label: '未发起', tone: 'gray' }, queued: { label: '等待处理', tone: 'warning' },
  running: { label: '找回处理中', tone: 'indigo' }, healthy: { label: '凭据正常', tone: 'success' },
  restored: { label: '已找回', tone: 'success' }, unrecoverable: { label: '不可恢复', tone: 'error' }, pending_check: { label: '待核验', tone: 'warning' },
};
export const credentialOptions = Object.entries(credentialLabels).map(([value, state]) => ({ value, label: state.label }));
export const reclaimOptions = Object.entries(reclaimLabels).map(([value, state]) => ({ value, label: state.label }));
export function credentialValue(value: string | null): CredentialState | undefined { return credentialOptions.find((item) => item.value === value)?.value as CredentialState | undefined; }
export function reclaimValue(value: string | null): ReclaimState | undefined { return reclaimOptions.find((item) => item.value === value)?.value as ReclaimState | undefined; }
export function reclaimOrigin(value?: string): string { return ({ customer: '客户', owner: '管理员', automatic_401: '系统', system: '系统', anonymous: '客户' } as Record<string, string>)[value ?? ''] ?? '未留存'; }
export function reclaimStage(value?: string): string { return ({ probe: '核验原凭据', refresh: '刷新凭据', relogin: '重新登录账号', publish: '保存找回结果' } as Record<string, string>)[value ?? ''] ?? '—'; }
export function oauthStatus(value: string): string { return ({ pending: '等待登录', generating: '登录中', reclaiming: '找回中', ready: '已保存', unavailable: '不可用' } as Record<string, string>)[value] ?? '待核验'; }
export function versionLabel(generation?: number): string { return generation ? `第 ${generation} 版` : '未留存'; }
