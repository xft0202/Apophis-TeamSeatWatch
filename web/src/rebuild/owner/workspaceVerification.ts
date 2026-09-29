import type { components } from '../../generated/owner';

type Verification = components['schemas']['SelectedWorkspaceVerification'];

export function canShowWorkspaceFacts(fact: Verification | null, now: number): boolean {
  return fact?.status === 'verified'
    && fact.permission === 'manage'
    && fact.accessStatus === 'readable'
    && fact.completeness === 'complete'
    && fact.expiresAt !== undefined
    && Number.isFinite(Date.parse(fact.expiresAt))
    && Date.parse(fact.expiresAt) > now;
}
