import type { components } from '../generated/owner';
import type { AccountProbeFilter } from './auth';

export const probeLabels: Record<AccountProbeFilter, string> = { available: '正常', credential_invalid: '凭据待复核', account_problem: '账号异常', definitely_unavailable: '不可用', transient_failure: '探测失败', unknown: '待核验', unprobed: '未探测' };

export function latestProbe(account: components['schemas']['TargetAccount']) {
  const status = account.latestProbeStatus ?? 'unprobed';
  const label = account.latestProbeErrorCode === 'personal_credential_absent' ? '未获取 AT' : status === 'definitely_unavailable' && account.latestProbeOrigin === 'personal' ? '已停用' : status === 'account_problem' && account.latestProbeOrigin === 'personal' ? '权限不足' : probeLabels[status];
  return { status, label, ...(account.latestProbedAt ? { at: account.latestProbedAt } : {}) };
}

export function accountAccent(identifier: string) {
  const domain = identifier.split('@')[1] ?? identifier;
  return ['indigo', 'mint', 'peach', 'rose'][[...domain].reduce((value, char) => value + char.charCodeAt(0), 0) % 4];
}
