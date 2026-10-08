export function oauthDiagnostic(code: string): string | undefined {
  const known: Record<string, string> = {
    oauth_generation_failed: 'OAuth 登录未完成，请重试',
    oauth_generator_unavailable: '授权服务暂不可用',
    oauth_configuration_invalid: '授权服务暂不可用',
    oauth_probe_failed: '目标空间凭据尚未核验通过',
    oauth_session_expired: '登录会话已失效，重新登录未完成，请重试',
    oauth_workspace_unavailable: '平台尚未允许此账号进入所选空间，邀请结果已保留',
    oauth_identity_failed: '授权账号或空间与本轮不一致，凭据未保存',
  };
  if (known[code]) return known[code];
  const match = /^oauth_(browser|workspace_select|authorize|token_exchange)_(failed|browser_challenge|http_\d{3})$/.exec(code);
  if (!match) return undefined;
  const stage: Record<string, string> = { browser: '建立登录会话', workspace_select: '选择本轮空间', authorize: '取得空间授权', token_exchange: '获取 RT / AT' };
  const action = stage[match[1] ?? ''] ?? 'OAuth 登录';
  if (match[2] === 'browser_challenge') return `${action}被平台浏览器校验拦截，请重试`;
  if (match[2]?.startsWith('http_')) return `${action}被平台拒绝（HTTP ${match[2].slice(5)}），请重试`;
  return `${action}未完成，请重试`;
}
