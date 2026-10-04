// The latest request for the current scope is the only response allowed to
// authorize a confirmation. Scope changes invalidate responses synchronously.
export class PersonalPreviewGate {
  private scopeKey = '';
  private request = 0;

  updateScope(scopeKey: string): void {
    if (scopeKey !== this.scopeKey) {
      this.scopeKey = scopeKey;
      this.request += 1;
    }
  }

  begin(scopeKey: string): number {
    this.updateScope(scopeKey);
    this.request += 1;
    return this.request;
  }

  accepts(scopeKey: string, request: number): boolean {
    return this.scopeKey === scopeKey && this.request === request;
  }

  async load<T>(
    scopeKey: string,
    fetchPreview: () => Promise<T>,
    onAccepted: (preview: T, request: number) => void,
    onError: (error: unknown) => void,
    onSettled: () => void,
  ): Promise<void> {
    const request = this.begin(scopeKey);
    try {
      const preview = await fetchPreview();
      if (this.accepts(scopeKey, request)) onAccepted(preview, request);
    } catch (error: unknown) {
      if (this.accepts(scopeKey, request)) onError(error);
    } finally {
      if (this.accepts(scopeKey, request)) onSettled();
    }
  }
}
