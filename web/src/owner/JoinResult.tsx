import { Stack, Text } from '@mantine/core';
import type { JoinOperation } from './batchOperations';
import { oauthDiagnostic } from './oauthDiagnostics';

type Target = JoinOperation['targets'][number];
const diagnostics: Record<string, string> = {
  seat_type_mismatch: '席位类型不符，要求高级席位', invitation_unconfirmed: '高级席位邀请待核验', invitation_rejected: '平台拒绝邀请',
  workspace_subscription_delinquent: '团队订阅账单逾期，平台拒绝邀请',
  premium_capacity_exceeded: '等待高级席位，邀请未发送', premium_capacity_unknown: '高级席位开通数未获取，请同步空间',
  workspace_authority_changed: '母号或空间状态已变化，请同步后核对', credential_invalid: '账号登录资料不可用',
  definitely_unavailable: '账号不可用', transient_failure: '暂时无法连接', platform_configuration_invalid: '平台服务暂不可用',
  platform_credential_unavailable: '账号资料无法读取', member_not_confirmed: '尚未确认加入空间', membership_unknown: '加入结果待核验',
  reconciliation_attempts_exhausted: '仍未确认加入空间', incomplete_response: '响应不完整，结果待核验', transport_failure: '请求未取得完整回执', proxy_egress_drift: '网络出口变化，操作暂停',
};
const describe = (code: string) => oauthDiagnostic(code) ?? diagnostics[code] ?? '未完成，请核对账号及空间状态';

export default function JoinResult({ target, phase }: { target: Target; phase: 'invitation' | 'oauth' }) {
  if (phase === 'oauth') return <Text size="sm">{describe(target.diagnosticCode ?? target.failureDiagnosticCode ?? '')}</Text>;
  const receipt = target.stageResults.find(item => item.stage === 'send_invitation');
  const failure = target.failureStage === 'send_invitation' ? target.failureDiagnosticCode : target.diagnosticCode;
  if (target.invitationConfirmedAt) return <Text size="sm">—</Text>;
  return <Stack gap={4} align="center">
    <Text size="sm">{failure ? describe(failure) : receipt?.requestSent && !receipt.success ? '邀请结果待核验' : '—'}</Text>
    {receipt?.httpStatus && receipt.httpStatus >= 400 ? <Text size="xs" c="dimmed">HTTP {receipt.httpStatus}</Text> : null}
  </Stack>;
}
