// A read cannot supersede a sent action. Invalidating a scope never cancels the
// remote obligation: it only prevents old responses from updating this panel.
export function createRotationJoinRequests() {
  let scope = 0;
  let sequence = 0;
  const actions = new Map<string, number>();
  return {
    invalidate() { scope++; sequence++; actions.clear(); },
    beginRead() { return actions.size ? null : { scope, sequence: ++sequence }; },
    isCurrentRead(token: { scope: number; sequence: number }) {
      return token.scope === scope && token.sequence === sequence && actions.size === 0;
    },
    beginAction(slot: string) {
      if (actions.has(slot)) return null;
      const token = { scope, sequence: ++sequence, slot };
      actions.set(slot, token.sequence);
      return token;
    },
    isCurrentAction(token: { scope: number; sequence: number; slot: string }) {
      return token.scope === scope && actions.get(token.slot) === token.sequence;
    },
    finishAction(token: { scope: number; sequence: number; slot: string }) {
      if (token.scope !== scope || actions.get(token.slot) !== token.sequence) return false;
      actions.delete(token.slot);
      return true;
    },
  };
}
