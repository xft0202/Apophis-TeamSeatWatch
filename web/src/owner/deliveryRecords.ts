import createClient from 'openapi-fetch';
import type { components, paths } from '../generated/owner';
import { mutationHeaders, OwnerApiError } from './auth';
import { formatDateTime } from '../shared/dateTime';

const api = createClient<paths>({ baseUrl: '', credentials: 'include' });

export type DeliveryRecord = components['schemas']['DeliveryRecord'];
export type DeliveryRecords = components['schemas']['DeliveryRecordList'];

function result<T>(response: { data?: T; error?: unknown; response: Response }): T {
  if (response.error || response.data === undefined) {
    throw new OwnerApiError(response.response.status, response.error);
  }
  return response.data;
}

export async function getDeliveryRecord(membershipId: string, signal?: AbortSignal): Promise<DeliveryRecord> {
  return result(await api.GET('/api/owner/v1/deliveries/{membershipId}', { ...(signal ? { signal } : {}), params: { path: { membershipId } } }));
}

export async function activateMembershipCard(membershipId: string, cardSecret: string, idempotencyKey: string) {
  return result(await api.POST('/api/owner/v1/memberships/{membershipId}/card', {
    params: { path: { membershipId }, header: await mutationHeaders() },
    body: { cardSecret, idempotencyKey },
  }));
}

export async function revokeDeliveryCard(membershipId: string) {
  return result(await api.POST('/api/owner/v1/deliveries/{membershipId}/card/revoke', {
    params: { path: { membershipId }, header: await mutationHeaders() },
    body: { confirm: true },
  }));
}

export function generateAccountCard(): string {
  const bytes = new Uint8Array(20);
  crypto.getRandomValues(bytes);
  const raw = String.fromCharCode(...bytes);
  return 'TSW1-' + btoa(raw).replaceAll('+', '-').replaceAll('/', '_').replaceAll('=', '');
}

export function recordStatus(record: DeliveryRecord): { tone: 'success' | 'warning' | 'error' | 'gray'; label: string } {
  switch (record.cardState) {
    case 'revoked': return { tone: 'error', label: '已撤销' };
    case 'claimed': return { tone: 'success', label: '已兑换' };
    case 'expired': return { tone: 'warning', label: '已过期' };
    case 'unavailable': return { tone: 'error', label: '不可兑换' };
    case 'unclaimed': return { tone: 'warning', label: '待兑换' };
    case 'unactivated': return { tone: 'gray', label: '待生成' };
  }
}

export function recordTime(value?: string): string {
  return value ? formatDateTime(value) : '—';
}
