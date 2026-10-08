import { Group, Text } from '@mantine/core';
import { accountAccent } from './accountPresentation';

export default function AccountIdentity({ identifier }: { identifier: string }) {
  return <Group gap={12} wrap="nowrap" justify="flex-start"><Text className="account-avatar" data-accent={accountAccent(identifier)} aria-hidden="true">{identifier.slice(0, 1).toUpperCase()}</Text><Text size="sm" fw={600} className="account-identifier" title={identifier}>{identifier}</Text></Group>;
}
