import type { components } from '../../generated/owner';

type Discovery = components['schemas']['MotherDiscovery'];
type VisibleWorkspace = components['schemas']['MotherVisibleWorkspace'];

export function isWorkspaceSelectable(workspace: VisibleWorkspace): boolean {
  return workspace.accessStatus === 'readable';
}

// Confirming a visible candidate is a local choice, not a grant of management rights.
export function confirmWorkspace(discovery: Discovery | null, candidateId: string): string | null {
  if (discovery?.status !== 'discovered' || !candidateId) return null;
  return discovery.workspaces.some((workspace) => workspace.id === candidateId && isWorkspaceSelectable(workspace))
    ? candidateId : null;
}
