import { proxyPoolApi } from './proxyPool';
import type { AccountFeedback } from './accountSession';

// Shared preflight for explicit account actions; proxy configuration stays in its domain.
export async function accountNetworkPrerequisite(): Promise<AccountFeedback | undefined> {
  const pool = await proxyPoolApi.capacity();
  if (pool.mode === 'direct' || pool.healthyCount > 0) return undefined;
  return { message: '没有可用代理，请先在代理管理配置并验证节点', tone: 'warning', proxy: true };
}
