import createClient from 'openapi-fetch';
import type { components, paths } from '../generated/owner';
import { mutationHeaders, OwnerApiError } from './auth';
import type { StatusTone } from '../shared/StatusBadge';
const api = createClient<paths>({ baseUrl: '', credentials: 'include' });
export type ProxyPool = components['schemas']['ProxyPool'];
export type ProxyNode = components['schemas']['ProxyPoolNode'];
export type SourceDraft = components['schemas']['ProxySourceRequest'];
export type SourceDiagnostic = components['schemas']['ProxySourceDiagnostic'];
export type ProxyState = ProxyNode['state'];
export type ProxySourceFilter = NonNullable<paths['/api/owner/v1/proxy-pool']['get']['parameters']['query']>['source_kind'];
function result<T>(response: { data?: T; error?: unknown; response: Response }): T {
  if (response.error || response.data === undefined) throw new OwnerApiError(response.response.status, response.error);
  return response.data;
}
export const proxyPoolApi = {
  capacity: async () => result(await api.GET('/api/owner/v1/proxy-pool/settings')),
  get: async (page = 1, pageSize = 20, signal?: AbortSignal, state?: ProxyState, sourceKind?: ProxySourceFilter) => result(await api.GET('/api/owner/v1/proxy-pool', { params: { query: { page, page_size: pageSize, ...(state ? { state } : {}), ...(sourceKind ? { source_kind: sourceKind } : {}) } }, ...(signal ? { signal } : {}) })),
  source: async (body: SourceDraft) => result(await api.PUT('/api/owner/v1/proxy-pool/source', { params: { header: await mutationHeaders() }, body })),
  test: async (body: SourceDraft) => result(await api.POST('/api/owner/v1/proxy-pool/source/test', { params: { header: await mutationHeaders() }, body })),
  sync: async () => result(await api.POST('/api/owner/v1/proxy-pool/sync', { params: { header: await mutationHeaders() } })),
  importNodes: async (proxies: string[]) => result(await api.POST('/api/owner/v1/proxy-pool/nodes/import', { params: { header: await mutationHeaders() }, body: { proxies } })),
  probe: async () => result(await api.POST('/api/owner/v1/proxy-pool/probe', { params: { header: await mutationHeaders() } })),
  probeNode: async (nodeId: string) => result(await api.POST('/api/owner/v1/proxy-pool/nodes/{nodeId}/probe', { params: { path: { nodeId }, header: await mutationHeaders() } })),
  remove: async (nodeId: string) => result(await api.DELETE('/api/owner/v1/proxy-pool/nodes/{nodeId}', { params: { path: { nodeId }, header: await mutationHeaders() } })),
  settings: async (input: { targetHealthy: number; probeConcurrency: number; taskConcurrency: number }) => result(await api.PATCH('/api/owner/v1/proxy-pool/settings', { params: { header: await mutationHeaders() }, body: input })),
};
export const sourceOptions: { value: SourceDraft['kind']; label: string }[] = [
  { value: 'cliproxy', label: 'CLIProxy' }, { value: 'b2proxy', label: 'B2Proxy' }, { value: 'proxy1024', label: '1024Proxy' },
  { value: 'subscription', label: '订阅链接' }, { value: 'direct', label: '直连模式' },
];
export const sourceFilterOptions = [{ value: 'manual', label: '手动代理' }, ...sourceOptions.filter(option => option.value !== 'direct')];
export function sourceLabel(kind: string): string { if (kind === 'manual') return '手动代理'; return sourceOptions.find(option => option.value === kind)?.label ?? kind; }
export function proxyStateLabel(state: ProxyNode['state']): { label: string; tone: StatusTone } {
  switch (state) {
    case 'healthy': return { label: '健康', tone: 'success' };
    case 'pending': return { label: '待验证', tone: 'warning' };
    default: return { label: '已隔离', tone: 'error' };
  }
}
export function proxyFailureLabel(code: string): string {
  const labels: Record<string, string> = {
    proxy_target_http_rejected: '目标拒绝访问', proxy_auth_rejected: '代理认证被拒绝', proxy_connection_failed: '代理连接失败',
    proxy_timeout: '连接超时', proxy_cancelled: '验证已取消', proxy_dns_failed: '域名解析失败', proxy_tls_failed: 'TLS 连接失败',
    proxy_browser_challenge: '平台要求浏览器验证，暂未通过', proxy_browser_unavailable: '平台核验浏览器暂不可用', proxy_browser_verification_incomplete: '平台浏览器验证未完成，请重试',
    proxy_ip_echo_invalid: '出口地址返回无效', proxy_ip_echo_non_public: '出口不是公网地址',
    egress_duplicate_exit: '与其他节点使用相同出口', egress_endpoint_invalid: '代理地址无效',
    proxy_transport_invalid: '代理连接配置无效', secret_unavailable: '代理凭据无法读取',
    proxy_session_expired: '会话稳定时长不足', proxy_session_unstable: '动态出口无法用于稳定会话',
    proxy_source_changed: '来源已切换', proxy_subscription_removed: '来源已移除该节点',
    proxy_subscription_unavailable: '订阅读取失败或格式不支持', invalid_proxy_source: '来源参数无效',
  };
  return labels[code] ?? '验证未通过';
}
export function diagnosticLabel(step: SourceDiagnostic['steps'][number]): string {
  const stage = ({ configuration: '配置', exit: '连接与出口核验', platform: '平台访问', session: '会话', subscription: '订阅读取' } as Record<string, string>)[step.stage] ?? '验证';
  return `${stage} · ${step.code ? proxyFailureLabel(step.code) : '通过'}${step.httpStatus ? ` · HTTP ${step.httpStatus}` : ''}`;
}
export function sessionRemaining(until?: string | null): string {
  if (!until) return '—';
  const seconds = Math.max(0, Math.floor((Date.parse(until) - Date.now()) / 1000));
  return seconds ? `${Math.floor(seconds / 60)}分 ${seconds % 60}秒` : '已到期';
}
