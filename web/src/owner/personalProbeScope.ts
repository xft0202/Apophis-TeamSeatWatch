// Cross-page selection wins over the search filter; never silently probe the visible page.
export function personalProbeScope(selected: ReadonlySet<string>, search: string) {
  return selected.size ? { targetAccountIds: [...selected].sort() } : { search };
}
