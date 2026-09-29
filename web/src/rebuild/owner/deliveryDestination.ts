import type { components } from '../../generated/owner';
import { mutationHeaders, OwnerApiError } from './auth';

export type Destination = components['schemas']['DeliveryDestination'];
export type TestOutcome = NonNullable<Destination['test']>['connection' | 'target'] | 'untested';
export type DestinationInput = Omit<components['schemas']['CreateDeliveryDestination'], 'secret'> & { secret?: string };
export type DestinationErrorCode = 'session_expired' | 'csrf_rejected' | 'invalid_destination' | 'destination_not_found' | 'destination_not_selectable' | 'destination_disabled' | 'test_stale' | 'delivery_unavailable';

export class DestinationError extends Error {
  readonly code: DestinationErrorCode;
  constructor(code: DestinationErrorCode) {
    super(code);
    this.code = code;
  }
}

async function request(path: string, method: 'GET' | 'POST' | 'PATCH', body?: unknown): Promise<Destination | Destination[]> {
  const headers: Record<string, string> = { Accept: 'application/json' };
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json';
    Object.assign(headers, await mutationHeaders());
  }
  const init: RequestInit = { method, credentials: 'include', headers };
  if (body !== undefined) init.body = JSON.stringify(body);
  const response = await fetch(path, init);
  const payload = await response.json().catch(() => undefined) as { items?: Destination[]; code?: string } | Destination | undefined;
  if (!response.ok) {
    const code = payload && 'code' in payload && typeof payload.code === 'string' ? payload.code : 'delivery_unavailable';
    throw new DestinationError(code as DestinationErrorCode);
  }
  if (!payload) throw new OwnerApiError(response.status, undefined);
  return payload && 'items' in payload ? payload.items ?? [] : payload as Destination;
}

export const destinationApi = {
  async list(): Promise<Destination[]> {
    return await request('/api/owner/v1/delivery-destinations', 'GET') as Destination[];
  },
  async create(input: DestinationInput): Promise<Destination> {
    return await request('/api/owner/v1/delivery-destinations', 'POST', input) as Destination;
  },
  async update(id: string, input: DestinationInput, enabled = true): Promise<Destination> {
    return await request(`/api/owner/v1/delivery-destinations/${encodeURIComponent(id)}`, 'PATCH', { ...input, enabled }) as Destination;
  },
  async setEnabled(id: string, enabled: boolean): Promise<Destination> {
    const current = (await this.list()).find((item) => item.id === id);
    if (!current) throw new DestinationError('destination_not_found');
    return await request(`/api/owner/v1/delivery-destinations/${encodeURIComponent(id)}`, 'PATCH', { name: current.name, endpoint: current.endpoint, targetGroup: current.targetGroup, enabled }) as Destination;
  },
  async test(id: string): Promise<Destination> {
    return await request(`/api/owner/v1/delivery-destinations/${encodeURIComponent(id)}/test`, 'POST', {}) as Destination;
  },
  async select(id: string): Promise<Destination> {
    return await request(`/api/owner/v1/delivery-destinations/${encodeURIComponent(id)}/select`, 'POST', {}) as Destination;
  },
};
