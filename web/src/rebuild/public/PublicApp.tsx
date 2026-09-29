import {
  Container,
  MantineProvider,
  Badge,
  Paper,
  Stack,
  Title,
} from '@mantine/core';
import { appTheme } from '../shared/theme';

export default function PublicApp() {
  return (
    <MantineProvider theme={appTheme}>
      <main className="public-page">
        <Container className="public-card" size={560} px={0}>
          <Paper withBorder radius="md" p={{ base: 'xl', sm: 40 }}>
            <div className="brand-lockup" aria-label="Apophis-TeamSeatWatch Public">
              <span className="brand-mark" aria-hidden="true">TS</span>
              <span className="brand-name">Apophis-TeamSeatWatch</span>
            </div>
            <Stack gap="sm" mt={36}>
              <Title order={1} size="h2">
                Public 兑换
              </Title>
              <Badge color="gray" variant="light" w="fit-content">
                暂未开放
              </Badge>
            </Stack>
          </Paper>
        </Container>
      </main>
    </MantineProvider>
  );
}
