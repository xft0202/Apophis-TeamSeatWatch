import type { components } from '../../generated/owner';

type Verification = components['schemas']['SelectedWorkspaceVerification'];

export function canShowWorkspaceFacts(fact: Verification | null, now: number): boolean {
  return fact?.status === 'verified'
    && (fact.permission === 'manage' || fact.permission === 'read')
    && fact.accessStatus === 'readable'
    && fact.completeness === 'complete'
    && fact.expiresAt !== undefined
    && Number.isFinite(Date.parse(fact.expiresAt))
    && Date.parse(fact.expiresAt) > now;
}

// Poll and mutation responses have separate fences. A poll begun before a POST
// cannot publish an old success while the POST is in flight or after it fails.
export function createWorkspaceRequestGate() {
  let pollSequence = 0;
  let mutationSequence = 0;
  let verifying = false;
  return {
    beginPoll(): number | null {
      return verifying ? null : ++pollSequence;
    },
    acceptPoll(sequence: number): boolean {
      return !verifying && sequence === pollSequence;
    },
    beginMutation(): number {
      verifying = true;
      pollSequence++;
      return ++mutationSequence;
    },
    isCurrentMutation(sequence: number): boolean {
      return sequence === mutationSequence;
    },
    acceptMutation(sequence: number): boolean {
      if (sequence !== mutationSequence) return false;
      verifying = false;
      return true;
    },
    invalidate(): void {
      pollSequence++;
      mutationSequence++;
      verifying = false;
    },
  };
}
