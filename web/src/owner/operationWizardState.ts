import type { Batch, DeliveryList, JoinOperation, JoinPreview } from './batchOperations';

export const operationSteps = ['选择对象', '发送邀请', 'OAuth 登录', '生成卡密', '本轮结果'] as const;
export type OperationStep = 0 | 1 | 2 | 3 | 4;

// Navigation follows whole-scope server counts, never the visible page's rows.
export function operationProgress(batch: Batch | null, invitation: JoinOperation | null, logins: DeliveryList | null) {
  const invited = Boolean(batch && invitation && invitation.targetTotal === batch.targetCount && invitation.invitationConfirmedCount > 0);
  const allInvited = Boolean(invited && invitation?.targetTotal === batch?.targetCount && invitation?.invitationConfirmedCount === batch?.targetCount);
  const loggedIn = Boolean(allInvited && invitation?.succeededCount === batch?.targetCount && logins && logins.total === batch?.targetCount && logins.readyCount === batch.targetCount);
  const cardsSaved = Boolean(loggedIn && logins && logins.cardCount === batch?.targetCount);
  const canGenerateCards = Boolean(batch && invitation && invitation.succeededCount > 0 && logins && logins.readyCount > 0);
  const canViewResults = Boolean(batch && logins && logins.cardCount > 0);
  const frontier: OperationStep = canViewResults ? 4 : canGenerateCards ? 3 : invited ? 2 : batch ? 1 : 0;
  return { invited, allInvited, loggedIn, cardsSaved, canGenerateCards, frontier };
}

export function operationActionFailure(problem: { status?: number | undefined; code?: string | undefined }, fallback: string) {
  const messages: Record<string, string> = {
    join_conflict: '所选账号存在重复执行，请核对账号并查看对应操作。',
    join_not_ready: '当前邀请条件未满足，请查看名单上方的具体原因。',
    invitation_incomplete: '尚无已确认邀请，请先发送邀请。',
    premium_capacity_exceeded: '高级席位已满，未发送的账号等待席位。',
    premium_capacity_unknown: '未获取高级席位开通数，请同步空间后继续邀请。',
    login_concurrency_frozen: '本轮 OAuth 登录仍在执行，请完成后再修改并发。',
    retry_not_available: '当前没有可安全重试的邀请，请查看原执行结果。',
    idempotency_conflict: '本次请求与已保存的操作范围不一致，请刷新核对。',
    workspace_not_manageable: '所选母号没有已确认的空间管理权限，请重新同步核对。',
  };
  if (problem.status === 401) return '登录已失效，请重新登录。';
  return (problem.code ? messages[problem.code] : undefined) ?? fallback;
}

export function invitationBlockers(preview: JoinPreview | null) {
  if (!preview) return [];
  const blockers = new Map(preview.blockers.map(blocker => [blocker.code, blocker]));
  const waiting = preview.waitingSeatCount ?? 0;
  if (waiting || blockers.has('premium_capacity_exceeded')) {
    const reason = preview.availablePremiumSeats === 0 ? '高级席位已满' : '高级席位不足';
    blockers.set('premium_capacity_exceeded', {
      code: 'premium_capacity_exceeded',
      message: `${reason}${waiting ? `，${waiting} 个账号等待席位` : ''}`,
    });
  }
  return [...blockers.values()];
}
