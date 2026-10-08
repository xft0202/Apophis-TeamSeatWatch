import createClient from 'openapi-fetch';
import type { components, paths } from '../generated/public';

export type DeliveryState = components['schemas']['RedeemState'];
export type Confirmation = components['schemas']['RedeemConfirmation'];
export type RecoveryState = components['schemas']['ReclaimStatus'];
export type CredentialStatus = components['schemas']['CredentialStatus'];
const api = createClient<paths>({ baseUrl: '', credentials: 'include' });
function result<T>(response: { data?: T; error?: unknown }): T {
  if (response.error || response.data === undefined) {
    const error = response.error;
    const code = typeof error === 'object' && error !== null && 'code' in error ? String(error.code) : 'public_unavailable';
    throw new Error(code);
  }
  return response.data;
}
export const publicApi = {
  confirm: async (cardSecret: string) => result(await api.POST('/api/public/v1/redeem/confirm', { body: { cardSecret } })),
  state: async () => result(await api.GET('/api/public/v1/redeem/state')),
  recover: async (cardSecret: string) => result(await api.POST('/api/public/v1/redeem/reclaim', { body: { cardSecret } })),
  recovery: async () => result(await api.GET('/api/public/v1/redeem/reclaim/status')),
  credentialStatus: async (cardSecret: string) => result(await api.POST('/api/public/v1/redeem/credential-status', { body: { cardSecret } })),
  download: async () => {
    const response = await fetch('/api/public/v1/redeem/download', { method: 'POST', credentials: 'include', headers: { Accept: 'application/zip' } });
    if (!response.ok) {
      let error: { code?: string } = {};
      try { error = await response.json(); } catch { /* stable fallback */ }
      throw new Error(error.code ?? 'public_unavailable');
    }
    const contentType = response.headers.get('Content-Type')?.split(';')[0];
    if (contentType !== 'application/zip') throw new Error('public_delivery_pending');
    const disposition = response.headers.get('Content-Disposition') ?? '';
    const filename = /filename="(Apophis-TeamSeatWatch-\d{4}(?:-\d{2}){5}\.zip)"/.exec(disposition)?.[1];
    if (!filename) throw new Error('public_delivery_pending');
    const blob = await response.blob();
    if (blob.size === 0) throw new Error('public_delivery_pending');
    return { blob, filename };
  },
};
export function publicErrorMessage(error: unknown): string {
  const code = error instanceof Error ? error.message : '';
  if (code === 'public_rate_limited') return '操作过于频繁';
  if (code === 'public_request_denied' || code === 'public_request_invalid') return '卡密不可用';
  if (code === 'public_delivery_pending') return '交付包暂不可下载';
  return '操作未完成';
}
