import type { components } from '../generated/owner';

type Draft = components['schemas']['OperationDraft'];

export function wizardStep(draft: Draft | null): 'start' | Draft['step'] {
  return draft?.step ?? 'start';
}

// Status never substitutes batch count for target-space eligibility or read access for write authority.
export function draftStatus(draft: Draft): string {
  if (draft.motherAccountId && !draft.motherCurrent) return '母号资料已变化；返回母号步骤重新选择。';
  if (draft.workspaceId && !draft.workspaceCurrent) return '空间入口或核验已变化；返回空间步骤重新核验并确认。';
  if (draft.batchId && !draft.batchCurrent) return '批次或子号归属已变化；返回子号步骤重新确认精确账号。';
  if (draft.destinationId && !draft.destinationCurrent) return '交付去向已变化或测试失效；返回去向步骤重新测试并确认。';
  return '';
}

export function canChooseDestination(draft: Draft): boolean {
  return draft.motherCurrent && draft.workspaceCurrent && draft.batchCurrent;
}
