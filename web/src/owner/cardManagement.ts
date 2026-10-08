import createClient from 'openapi-fetch';
import type { components, paths } from '../generated/owner';
import { mutationHeaders, OwnerApiError } from './auth';
import type { DeliveryRecord } from './deliveryRecords';

const api = createClient<paths>({ baseUrl: '', credentials: 'include' });
export type CardState = components['schemas']['CardState'];
export type CardSelectionEntry = components['schemas']['CardSelectionEntry'];
export type CardQuery = NonNullable<paths['/api/owner/v1/deliveries']['get']['parameters']['query']>;
function result<T>(response: { data?: T; error?: unknown; response: Response }): T {
  if (response.error || response.data === undefined) throw new OwnerApiError(response.response.status, response.error);
  return response.data;
}
export const cardManagementApi = {
  list: async (query: CardQuery, signal?: AbortSignal) => result(await api.GET('/api/owner/v1/deliveries', { ...(signal ? { signal } : {}), params: { query: { ...query, cards_only: true } } })),
  select: async (query: CardQuery, signal?: AbortSignal) => result(await api.GET('/api/owner/v1/deliveries/card-selection', { ...(signal ? { signal } : {}), params: { query } })),
  secret: async (record: DeliveryRecord) => result(await api.GET('/api/owner/v1/deliveries/{membershipId}/card-secret', { params: { path: { membershipId: record.membershipId }, query: { card_version: record.cardVersion ?? 0 } } })),
  export: async (motherAccountId: string, workspaceId: string, items: CardSelectionEntry[]) => result(await api.POST('/api/owner/v1/deliveries/card-export', { params: { header: await mutationHeaders() }, body: { motherAccountId, workspaceId, items } })),
};
export const cardStateOptions: Array<{ value: CardState; label: string }> = [
  { value: 'unclaimed', label: '待兑换' }, { value: 'claimed', label: '已兑换' },
  { value: 'revoked', label: '已撤销' }, { value: 'expired', label: '已过期' }, { value: 'unavailable', label: '不可兑换' },
];
export function cardStateValue(value: string | null): CardState | undefined {
  return cardStateOptions.find((item) => item.value === value)?.value;
}
