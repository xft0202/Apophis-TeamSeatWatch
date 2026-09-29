import {
  Container,
  MantineProvider,
  Paper,
  Stack,
  Text,
  Title,
} from '@mantine/core';
import { appTheme } from '../shared/theme';

export default function PublicApp() {
  return (
    <MantineProvider theme={appTheme}>
      <main className="public-page">
        <Container className="public-card" size={560} px={0}>
          <Paper withBorder shadow="sm" radius="md" p={{ base: 'xl', sm: 40 }}>
            <div className="brand-lockup" aria-label="Apophis-TeamSeatWatch Public">
              <span className="brand-mark" aria-hidden="true">TS</span>
              <span className="brand-name">Apophis-TeamSeatWatch</span>
            </div>
            <Stack gap="sm" mt={36}>
              <Title order={1} size="h2">
                Public 入口正在重建
              </Title>
              <Text c="dimmed" maw={480}>
                当前切片只建立全新的单栈 Owner 登录入口。旧兑换页面不会迁移到新端，后续 Public
                能力将按对应票据独立实现。
              </Text>
            </Stack>
          </Paper>
        </Container>
      </main>
    </MantineProvider>
  );
}
