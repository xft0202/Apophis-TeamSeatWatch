// Local simulation contract for Ticket 08. There is no delivery-destination Owner API yet.
// This store never sends requests or customer credentials; a backend must replace it before production use.
export type TestOutcome = 'untested' | 'connected' | 'connection_failed' | 'permission_denied' | 'target_mismatch';
export type Destination = Readonly<{
  id: string;
  name: string;
  endpoint: string;
  targetGroup: string;
  enabled: boolean;
  hasSecret: boolean;
  revision: number;
  test: { connection: TestOutcome; target: TestOutcome; revision: number } | null;
}>;
export type DestinationInput = { name: string; endpoint: string; targetGroup: string; secret?: string };
export type Probe = (input: Readonly<Required<DestinationInput>>) => Promise<{
  connection: 'connected' | 'connection_failed' | 'permission_denied';
  target: 'connected' | 'target_mismatch' | 'permission_denied' | 'untested';
}>;
export type DestinationErrorCode = 'permission_denied' | 'invalid_destination' | 'not_found' | 'not_selectable' | 'test_stale';

export class DestinationError extends Error {
  readonly code: DestinationErrorCode;
  constructor(code: DestinationErrorCode) {
    super(code);
    this.code = code;
  }
}

// An injected fixture supplies outcomes. The default denies tests rather than claiming remote connectivity.
export const unavailableProbe: Probe = async () => ({ connection: 'connection_failed', target: 'untested' });

export function createDestinationStore(probe: Probe = unavailableProbe) {
  type Stored = { id: string; name: string; endpoint: string; targetGroup: string; secret: string; enabled: boolean; revision: number; test: Destination['test'] };
  const records = new Map<string, Stored>();
  let selectedId: string | null = null;
  const publicRecord = (record: Stored): Destination => ({
    id: record.id, name: record.name, endpoint: record.endpoint, targetGroup: record.targetGroup,
    hasSecret: Boolean(record.secret), enabled: record.enabled, revision: record.revision,
    test: record.test ? { ...record.test } : null,
  });
  const requireOwner = (authorized: boolean) => { if (!authorized) throw new DestinationError('permission_denied'); };
  const find = (id: string) => {
    const record = records.get(id);
    if (!record) throw new DestinationError('not_found');
    return record;
  };
  const validate = (input: DestinationInput, existing?: Stored) => {
    const name = input.name.trim();
    const endpoint = input.endpoint.trim();
    const targetGroup = input.targetGroup.trim();
    const secret = input.secret === undefined || input.secret === '' ? existing?.secret ?? '' : input.secret;
    // Do not put user input, URL or secrets in validation errors.
    if (!name || !targetGroup || !secret || !/^https:\/\/[^\s/@?#]+(?::\d+)?(?:\/[^\s]*)?$/.test(endpoint)) {
      throw new DestinationError('invalid_destination');
    }
    return { name, endpoint, targetGroup, secret };
  };
  return {
    list(authorized: boolean): Destination[] {
      requireOwner(authorized);
      return [...records.values()].map(publicRecord);
    },
    selected(authorized: boolean): string | null { requireOwner(authorized); return selectedId; },
    create(authorized: boolean, input: DestinationInput): Destination {
      requireOwner(authorized);
      const values = validate(input);
      const record: Stored = { id: crypto.randomUUID(), ...values, enabled: true, revision: 1, test: null };
      records.set(record.id, record);
      return publicRecord(record);
    },
    update(authorized: boolean, id: string, input: DestinationInput): Destination {
      requireOwner(authorized);
      const record = find(id);
      Object.assign(record, validate(input, record));
      record.revision++;
      record.test = null;
      if (selectedId === id) selectedId = null;
      return publicRecord(record);
    },
    setEnabled(authorized: boolean, id: string, enabled: boolean): Destination {
      requireOwner(authorized);
      const record = find(id);
      if (record.enabled !== enabled) {
        record.enabled = enabled;
        record.revision++;
        record.test = null;
        if (selectedId === id) selectedId = null;
      }
      return publicRecord(record);
    },
    async test(authorized: boolean, id: string): Promise<Destination> {
      requireOwner(authorized);
      const record = find(id);
      if (!record.enabled) throw new DestinationError('not_selectable');
      const revision = record.revision;
      const result = await probe({ name: record.name, endpoint: record.endpoint, targetGroup: record.targetGroup, secret: record.secret });
      // A late probe cannot certify an edited or disabled destination.
      if (record.revision !== revision || !record.enabled) throw new DestinationError('test_stale');
      record.test = { ...result, revision };
      if (selectedId === id && (result.connection !== 'connected' || result.target !== 'connected')) selectedId = null;
      return publicRecord(record);
    },
    select(authorized: boolean, id: string): Destination {
      requireOwner(authorized);
      const record = find(id);
      if (!record.enabled || record.test?.revision !== record.revision || record.test.connection !== 'connected' || record.test.target !== 'connected') {
        throw new DestinationError('not_selectable');
      }
      selectedId = id;
      return publicRecord(record);
    },
  };
}

// Shared for the isolated rebuild only; simulates persistence across view unmounts, not across reloads.
export const destinationStore = createDestinationStore();
