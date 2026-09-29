import {
  Alert,
  Badge,
  Button,
  Container,
  Divider,
  Group,
  Loader,
  MantineProvider,
  Paper,
  PasswordInput,
  Stack,
  Text,
  TextInput,
  Title,
} from '@mantine/core';
import { useEffect, useState, type ChangeEvent, type SubmitEvent } from 'react';
import type { components } from '../../generated/owner';
import {
  clearCsrf,
  getAuthStatus,
  loginOwner,
  logoutOwner,
  ownerProblem,
  refreshOwnerSession,
} from './auth';
import { appTheme } from '../shared/theme';

type AuthStatus = components['schemas']['AuthStatus'];
type AuthState =
  | { kind: 'loading' }
  | { kind: 'signed-out'; notice?: string }
  | { kind: 'signed-in'; status: AuthStatus };

type LoginFields = {
  username: string;
  password: string;
};

const initialFields: LoginFields = { username: '', password: '' };

export default function OwnerApp() {
  const [auth, setAuth] = useState<AuthState>({ kind: 'loading' });

  useEffect(() => {
    let active = true;
    void getAuthStatus()
      .then((status) => {
        if (active) setAuth({ kind: 'signed-in', status });
      })
      .catch((error: unknown) => {
        if (!active) return;
        const problem = ownerProblem(error);
        if (problem.status === 401) {
          setAuth({ kind: 'signed-out' });
        } else {
          setAuth({
            kind: 'signed-out',
            notice: '登录状态暂时无法确认，请检查服务后重试。',
          });
        }
      });

    return () => {
      active = false;
    };
  }, []);

  if (auth.kind === 'loading') {
    return (
      <MantineProvider theme={appTheme}>
        <div className="auth-loading" role="status" aria-label="正在确认登录状态">
          <Loader color="indigo" size="sm" />
        </div>
      </MantineProvider>
    );
  }

  return (
    <MantineProvider theme={appTheme}>
      {auth.kind === 'signed-in' ? (
        <SignedInView
          status={auth.status}
          onSignedOut={() => setAuth({ kind: 'signed-out' })}
        />
      ) : (
        <LoginView
          initialNotice={auth.notice}
          onSignedIn={(status) => setAuth({ kind: 'signed-in', status })}
        />
      )}
    </MantineProvider>
  );
}

function LoginView({
  initialNotice,
  onSignedIn,
}: {
  initialNotice: string | undefined;
  onSignedIn: (status: AuthStatus) => void;
}) {
  const [fields, setFields] = useState(initialFields);
  const [notice, setNotice] = useState(initialNotice ?? '');
  const [pending, setPending] = useState(false);

  async function submit(event: SubmitEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!fields.username.trim() || !fields.password) {
      setNotice('请输入用户名和密码。');
      return;
    }

    setNotice('');
    setPending(true);
    try {
      await loginOwner({
        username: fields.username,
        password: fields.password,
      });
      const status = await getAuthStatus();
      onSignedIn(status);
    } catch (error: unknown) {
      const problem = ownerProblem(error);
      if (problem.code === 'csrf_rejected') clearCsrf();
      if (problem.status === 429) {
        setNotice(`尝试次数过多，请在 ${problem.retryAfterSeconds ?? 900} 秒后重试。`);
      } else if (problem.status === undefined || problem.status === 403 || problem.status >= 500) {
        setNotice('登录服务暂时不可用，请稍后重试。');
      } else {
        setNotice('用户名或密码不正确。');
      }
    } finally {
      setPending(false);
    }
  }

  return (
    <main className="auth-page">
      <Container className="auth-card" size={456} px={0}>
        <Paper withBorder shadow="sm" radius="md" p={{ base: 'xl', sm: 40 }}>
          <div className="brand-lockup" aria-label="Apophis-TeamSeatWatch Owner">
            <span className="brand-mark" aria-hidden="true">TS</span>
            <span className="brand-name">Apophis-TeamSeatWatch</span>
          </div>

          <Title className="auth-title" order={1} size="h2">
            Owner 登录
          </Title>
          <Text className="auth-subtitle" size="sm">
            使用本机 Owner 账号进入席位运营控制台。此入口不提供公网注册。
          </Text>

          {notice ? (
            <Alert color="red" title="无法登录" mt="xl">
              {notice}
            </Alert>
          ) : null}

          <form className="auth-form" onSubmit={submit} noValidate>
            <Stack gap="lg">
              <TextInput
                label="用户名"
                placeholder="输入 Owner 用户名"
                autoComplete="username"
                autoFocus
                maxLength={254}
                required
                value={fields.username}
                onChange={(event: ChangeEvent<HTMLInputElement>) => setFields({ ...fields, username: event.currentTarget.value })}
              />
              <PasswordInput
                label="密码"
                placeholder="输入密码"
                autoComplete="current-password"
                maxLength={1024}
                required
                value={fields.password}
                onChange={(event: ChangeEvent<HTMLInputElement>) => setFields({ ...fields, password: event.currentTarget.value })}
              />
              <Button type="submit" loading={pending} fullWidth size="md">
                登录
              </Button>
            </Stack>
          </form>

          <Text className="auth-footnote">
            会话由本机服务签发；请求继续使用 CSRF 双提交保护。
          </Text>
        </Paper>
      </Container>
    </main>
  );
}

function SignedInView({
  status,
  onSignedOut,
}: {
  status: AuthStatus;
  onSignedOut: () => void;
}) {
  const [pendingAction, setPendingAction] = useState<'refresh' | 'logout' | null>(null);
  const [message, setMessage] = useState('');

  async function refresh() {
    setPendingAction('refresh');
    setMessage('');
    try {
      await refreshOwnerSession();
      setMessage('会话已刷新。');
    } catch (error: unknown) {
      const problem = ownerProblem(error);
      if (problem.status === 401) {
        clearCsrf();
        onSignedOut();
      } else {
        setMessage('会话刷新失败，请稍后重试。');
      }
    } finally {
      setPendingAction(null);
    }
  }

  async function logout() {
    setPendingAction('logout');
    setMessage('');
    try {
      await logoutOwner();
      clearCsrf();
      onSignedOut();
    } catch (error: unknown) {
      const problem = ownerProblem(error);
      if (problem.status === 401) {
        clearCsrf();
        onSignedOut();
      } else {
        setMessage('退出失败，请稍后重试。');
      }
    } finally {
      setPendingAction(null);
    }
  }

  return (
    <div className="session-page">
      <header className="session-header">
        <Container className="session-header-inner" size="lg" px={{ base: 'md', sm: 'xl' }}>
          <div className="brand-lockup">
            <span className="brand-mark" aria-hidden="true">TS</span>
            <span className="brand-name">Apophis-TeamSeatWatch</span>
          </div>
          <Button variant="subtle" color="dark" onClick={logout} loading={pendingAction === 'logout'}>
            退出登录
          </Button>
        </Container>
      </header>

      <main className="session-main">
        <Container size="md" px={{ base: 'md', sm: 'xl' }}>
          <Stack gap="xl">
            <div>
              <div className="session-status">会话已确认</div>
              <Title order={1} size="h2" mt="md">
                Owner 控制台入口
              </Title>
              <Text c="dimmed" mt="xs">
                本切片只建立可靠的登录、会话确认、刷新和退出边界；业务页面将在后续票据中从零实现。
              </Text>
            </div>

            <Paper withBorder shadow="sm" radius="md" p={{ base: 'lg', sm: 'xl' }}>
              <Stack gap="lg">
                <Group justify="space-between" align="flex-start" wrap="nowrap">
                  <div>
                    <Text size="xs" tt="uppercase" fw={700} c="dimmed" lts="0.08em">
                      当前身份
                    </Text>
                    <Text className="session-username" size="lg" fw={600} mt={4}>
                      {status.username}
                    </Text>
                  </div>
                  <Badge color="green" variant="light">
                    已登录
                  </Badge>
                </Group>
                <Divider />
                <Group justify="space-between" align="center" gap="md">
                  <Text size="sm" c="dimmed">
                    服务端已确认此会话仍然有效。
                  </Text>
                  <Button
                    variant="light"
                    onClick={refresh}
                    loading={pendingAction === 'refresh'}
                    disabled={pendingAction !== null}
                  >
                    刷新会话
                  </Button>
                </Group>
                {message ? <Alert color="blue">{message}</Alert> : null}
              </Stack>
            </Paper>
          </Stack>
        </Container>
      </main>
    </div>
  );
}
