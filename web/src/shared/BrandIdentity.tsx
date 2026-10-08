import { Box, Group, Text } from '@mantine/core';
import { useResizeObserver } from '@mantine/hooks';
import symbol from './assets/teamseatwatch.svg';
import './brand.css';

export default function BrandIdentity({ inverse = false }: { inverse?: boolean }) {
  const [ref, { width }] = useResizeObserver<HTMLDivElement>();
  return <Group ref={ref} className={`brand-identity${inverse ? ' brand-identity-inverse' : ''}`} style={{ '--brand-color-width': width ? `${width}px` : undefined }} gap={8} wrap="nowrap" role="img" aria-label="TeamSeatWatch">
    <Box component="span" className="brand-symbol" aria-hidden="true" style={{ maskImage: `url(${symbol})` }} />
    <Text component="span" className="brand-wordmark brand-rainbow-text" aria-hidden="true">TeamSeatWatch</Text>
  </Group>;
}
