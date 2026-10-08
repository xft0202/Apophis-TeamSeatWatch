import type { components } from '../generated/owner';
const actions: Record<string, string> = {
  'oauth.attempt_started': '开始账号登录或核验', 'oauth.attempt_settled': '完成账号登录或核验', 'oauth.delivery_published': '保存账号登录结果',
  'oauth.reclaim_authorized': '发起401找回', owner_reauthorization: '发起401找回', owner_revoked: '撤销卡密',
  order_created: '兑换卡密', order_restored: '恢复领取', reclaim_status: '401找回进度',
  'card.activated': '生成卡密', activated: '生成卡密',
  'owner.card_revoked': '撤销卡密', card_revoke: '撤销卡密', revoke: '撤销卡密',
  'claim_succeeded': '兑换卡密', 'claim_denied': '兑换卡密', first_claim: '兑换卡密', redeem: '兑换卡密',
  'restore_succeeded': '恢复领取', 'restore_denied': '恢复领取', order_restore: '恢复领取',
  'owner.delivery_reclaim_authorized': '发起401找回', 'reclaim_requested': '发起401找回', oauth_reclaim: '401找回',
  'oauth.generation_started': '账号登录', 'oauth.generation_published': '保存账号登录结果', 'oauth.generation_failed': '账号登录',
  'oauth.probe_finished': '核验账号凭据', 'oauth.reclaim_started': '开始401找回', 'oauth.reclaim_finished': '完成401找回',
  generate: '账号登录', probe: '核验账号凭据', reclaim: '401找回',
  reclaim_request: '发起401找回', status_check: '核验交付凭据',
  delivery_version_saved: '保存交付版本', service_ended: '结束账号服务',
  download: '下载账号交付', download_authorized: '下载账号交付',
};
const results: Record<string, string> = {
  started: '已开始', unknown: '待核验', pending: '待处理', checking: '核验中', lease_lost: '结果待核验',
  refreshed: '凭据已刷新', repaired: '已修复', complete: '已完成', unavailable: '不可用',
  activated: '已生成', claimed: '已兑换', revoked: '已撤销', succeeded: '成功', success: '成功',
  published: '已保存', failed: '失败', denied: '未通过', queued: '已提交', running: '处理中',
  probe_ok: '凭据正常', token_refresh: '凭据已刷新', full_relogin: '已重新登录', unrecoverable: '无法找回',
  skipped: '已跳过', conflict: '状态已变化', accepted: '已提交', ok: '正常',
  healthy: '凭据正常', restored: '已找回', need_reclaim: '需要401找回',
  cannot_reclaim: '无法找回', ended: '已结束', authorized: '已允许下载',
};
export function deliveryEventLabels(event: components['schemas']['DeliveryRecordEvent']) {
  let action = actions[event.action] ?? '更新交付记录';
  if (event.action === 'oauth.attempt_started') action = event.stage === 'reclaim' ? '开始401找回' : event.stage === 'probe' ? '核验账号凭据' : '开始账号登录';
  if (event.action === 'oauth.attempt_settled' && event.stage === 'reclaim') {
    action = event.status === 'pending' ? ({ probe: '核验原凭据', refresh: '刷新凭据', relogin: '重新登录账号', publish: '保存找回结果' } as Record<string, string>)[event.result] ?? '处理401找回' : '完成401找回';
  }
  return { action, result: results[event.result] ?? results[event.status ?? ''] ?? '已记录' };
}
