import {
  Alert,
  Badge,
  Button,
  Card,
  Container,
  Divider,
  Group,
  Loader,
  MantineProvider,
  Paper,
  PasswordInput,
  Stack,
  Switch,
  Tabs,
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
import MotherMaterialsView from './MotherMaterialsView';
import ChildMaterialsView from './ChildMaterialsView';
import WorkspaceDiscoveryView from './WorkspaceDiscoveryView';
import StandbyChildBatchesView from './StandbyChildBatchesView';
import {
  destinationApi,
  type Destination,
  type DestinationError,
  type TestOutcome,
} from './deliveryDestination';

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
        <Paper withBorder radius={12} p={{ base: 'xl', sm: 40 }}>
          <div className="brand-lockup" aria-label="Apophis-TeamSeatWatch Owner">
            <span className="brand-mark" aria-hidden="true">TS</span>
            <span className="brand-name">Apophis-TeamSeatWatch</span>
          </div>

          <Title className="auth-title" order={1} size="h2">
            Owner 登录
          </Title>
          {notice ? (
            <Alert color="error" title="无法登录" mt="xl">
              {notice}
            </Alert>
          ) : null}

          <form className="auth-form" onSubmit={submit} noValidate>
            <Stack gap="lg">
              <TextInput
                label="用户名"
                placeholder="输入 Owner 用户名"
                radius={6}
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
                radius={6}
                autoComplete="current-password"
                maxLength={1024}
                required
                value={fields.password}
                onChange={(event: ChangeEvent<HTMLInputElement>) => setFields({ ...fields, password: event.currentTarget.value })}
              />
              <Button type="submit" loading={pending} fullWidth size="md" radius={6}>
                登录
              </Button>
            </Stack>
          </form>

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
  const [activeTab, setActiveTab] = useState<string | null>('workspaces');

  useEffect(() => {
    let active = true;
    const revalidate = async () => {
      try {
        await getAuthStatus();
      } catch (error: unknown) {
        if (active && ownerProblem(error).status === 401) {
          clearCsrf();
          onSignedOut();
        }
      }
    };
    const interval = window.setInterval(() => void revalidate(), 60_000);
    const onVisibilityChange = () => {
      if (document.visibilityState === 'visible') void revalidate();
    };
    document.addEventListener('visibilitychange', onVisibilityChange);
    return () => {
      active = false;
      window.clearInterval(interval);
      document.removeEventListener('visibilitychange', onVisibilityChange);
    };
  }, [onSignedOut]);

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
          <Button variant="subtle" color="dark" radius={6} onClick={logout} loading={pendingAction === 'logout'}>
            退出登录
          </Button>
        </Container>
      </header>

      <main className="session-main">
        <Container size="lg" px={{ base: 'md', sm: 'xl' }}>
          <Stack gap="xl">
            <div>
              <div className="session-status">会话已确认</div>
              <Title order={1} size="h2" mt="md">Owner 控制台</Title>
              <Text c="dimmed" mt="xs">{status.username}</Text>
            </div>

            <Paper withBorder radius={12} p={{ base: 'lg', sm: 'xl' }}>
              <Stack gap="lg">
                <Group justify="space-between" align="flex-start" wrap="nowrap">
                  <div>
                    <Text size="xs" tt="uppercase" fw={600} c="dimmed" lts="0.08em">当前身份</Text>
                    <Text className="session-username" size="lg" fw={600} mt={4}>{status.username}</Text>
                  </div>
                  <Badge color="success" variant="light">已登录</Badge>
                </Group>
                <Divider />
                <Group justify="space-between" align="center" gap="md">
                  <Text size="sm" c="dimmed">当前登录有效。</Text>
                  <Button variant="light" radius={6} onClick={refresh} loading={pendingAction === 'refresh'} disabled={pendingAction !== null}>刷新会话</Button>
                </Group>
                {message ? <Alert color="indigo">{message}</Alert> : null}
              </Stack>
            </Paper>

            <Tabs value={activeTab} onChange={setActiveTab} keepMounted={false}>
              <Tabs.List>
                <Tabs.Tab value="workspaces">空间管理</Tabs.Tab>
                <Tabs.Tab value="materials">母号资料</Tabs.Tab>
                <Tabs.Tab value="child">子号资料</Tabs.Tab>
                <Tabs.Tab value="standby">待用批次</Tabs.Tab>
                <Tabs.Tab value="destinations">交付去向</Tabs.Tab>
              </Tabs.List>
              <Tabs.Panel value="workspaces" pt="xl"><WorkspaceDiscoveryView onNextAction={() => setActiveTab('child')} /></Tabs.Panel>
              <Tabs.Panel value="materials" pt="xl"><MotherMaterialsView /></Tabs.Panel>
              <Tabs.Panel value="child" pt="xl"><ChildMaterialsView /></Tabs.Panel>
              <Tabs.Panel value="standby" pt="xl"><StandbyChildBatchesView /></Tabs.Panel>
              <Tabs.Panel value="destinations" pt="xl"><DeliveryDestinationPanel /></Tabs.Panel>
            </Tabs>
          </Stack>
        </Container>
      </main>
    </div>
  );
}

type DestinationForm = { name: string; endpoint: string; targetGroup: string; secret: string };
const blankDestinationForm: DestinationForm = { name: '', endpoint: '', targetGroup: '', secret: '' };

function destinationErrorMessage(error: unknown): string {
  const code = (error as Partial<DestinationError>).code;
  switch (code) {
    case 'session_expired': return 'Owner 会话已过期，请重新登录。';
    case 'csrf_rejected': return '安全校验失败，请刷新页面后重试。';
    case 'invalid_destination': return '请填写名称、以 /api/v1 结尾的 HTTPS Hub 地址、正整数目标组 ID 和连接密钥。不要填写客户账号密码或 2FA。';
    case 'destination_not_found': return '该交付去向已不存在，请刷新列表。';
    case 'destination_not_selectable': return '必须先启用并通过连接、目标组两项测试，才能选择去向。';
    case 'destination_disabled': return '请先启用去向，再测试连接与目标组。';
    case 'test_stale': return '测试完成前配置已变化，请重新测试当前配置。';
    default: return '操作未完成，请稍后重试。';
  }
}

function outcomeLabel(outcome: TestOutcome): { label: string; color: 'gray' | 'green' | 'red' | 'yellow' } {
  switch (outcome) {
    case 'connected': return { label: '通过', color: 'green' };
    case 'connection_failed': return { label: '连接失败：检查地址或网络', color: 'red' };
    case 'permission_denied': return { label: '权限不足：检查去向授权', color: 'red' };
    case 'target_mismatch': return { label: '目标组不匹配：检查目标范围', color: 'yellow' };
    default: return { label: '未测试', color: 'gray' };
  }
}

function DestinationTestSummary({ destination }: { destination: Destination }) {
  const connection = outcomeLabel(destination.test?.connection ?? 'untested');
  const target = outcomeLabel(destination.test?.target ?? 'untested');
  return (
    <Stack gap={4} mt="sm">
      <Group gap="xs"><Text size="sm" c="dimmed">连接</Text><Badge color={connection.color} variant="light">{connection.label}</Badge></Group>
      <Group gap="xs"><Text size="sm" c="dimmed">目标组</Text><Badge color={target.color} variant="light">{target.label}</Badge></Group>
    </Stack>
  );
}

function DeliveryDestinationPanel() {
  const [destinations, setDestinations] = useState<Destination[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [form, setForm] = useState<DestinationForm>(blankDestinationForm);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [wizardOpen, setWizardOpen] = useState(false);
  const [notice, setNotice] = useState('');
  const [pending, setPending] = useState<string | null>(null);

  const sync = async () => {
    const items = await destinationApi.list();
    setDestinations(items);
    setSelectedId(items.find((item) => item.selected)?.id ?? null);
  };
  useEffect(() => { void sync().catch((error: unknown) => setNotice(destinationErrorMessage(error))); }, []);
  const performAction = async (key: string, action: () => unknown | Promise<unknown>) => {
    setPending(key); setNotice('');
    try { await action(); await sync(); } catch (error: unknown) { setNotice(destinationErrorMessage(error)); } finally { setPending(null); }
  };
  const submit = (event: SubmitEvent<HTMLFormElement>) => {
    event.preventDefault();
    void performAction('save', () => {
      const operation = editingId
        ? destinationApi.update(editingId, form, destinations.find((destination) => destination.id === editingId)?.enabled ?? true)
        : destinationApi.create(form);
      return operation.then(() => {
        setForm(blankDestinationForm); setEditingId(null); setWizardOpen(false);
      });
    });
  };
  const startEdit = (destination: Destination) => {
    setEditingId(destination.id);
    setForm({ name: destination.name, endpoint: destination.endpoint, targetGroup: destination.targetGroup, secret: '' });
    setWizardOpen(false); setNotice('');
  };
  const cancelEdit = () => { setEditingId(null); setForm(blankDestinationForm); setWizardOpen(false); };

  return (
    <section aria-labelledby="destination-heading">
      <Group justify="space-between" align="flex-end" mb="md">
        <div>
          <Text size="xs" tt="uppercase" fw={600} c="dimmed" lts="0.08em">交付去向</Text>
          <Title id="destination-heading" order={2} size="h3" mt={4}>连接与目标范围</Title>
        </div>
        {!destinations.length ? <Button radius={6} onClick={() => setWizardOpen(true)}>开始配置</Button> : <Button variant="light" radius={6} onClick={() => { setWizardOpen(true); setEditingId(null); setForm(blankDestinationForm); }}>添加去向</Button>}
      </Group>
      <Alert color="yellow" mb="lg" title="安全边界">这里只保存交付渠道连接秘密；不会收集、回显或发送客户账号密码、Cookie、2FA 或其它客户凭据。通过连接测试也不代表已经完成交付。</Alert>
      {notice ? <Alert color="red" mb="lg" title="操作未完成">{notice}</Alert> : null}

      {!destinations.length && !wizardOpen ? (
        <Paper withBorder radius={12} p={{ base: 'lg', sm: 'xl' }}>
          <Stack gap="sm"><Title order={3} size="h4">还没有可用的交付去向</Title><Text c="dimmed">按向导完成连接、目标组和可用性测试后，才能选择交付去向。</Text><Group><Button radius={6} onClick={() => setWizardOpen(true)}>进入配置向导</Button></Group></Stack>
        </Paper>
      ) : null}

      {wizardOpen ? (
        <Paper withBorder radius={12} p={{ base: 'lg', sm: 'xl' }} mb="lg">
          <form onSubmit={submit} noValidate>
            <Stack gap="md">
              <div><Title order={3} size="h4">{editingId ? '修改交付去向' : '配置交付去向'}</Title><Text size="sm" c="dimmed" mt={4}>保存后仍需重新测试；任何配置变更都会使旧测试结果失效。</Text></div>
              <TextInput label="名称" required value={form.name} onChange={(event) => setForm({ ...form, name: event.currentTarget.value })} />
              <TextInput label="Sub2API Hub HTTPS 地址" placeholder="https://hub.example.test/api/v1" required value={form.endpoint} onChange={(event) => setForm({ ...form, endpoint: event.currentTarget.value })} />
              <TextInput label="目标组 ID" placeholder="例如：42" required value={form.targetGroup} onChange={(event) => setForm({ ...form, targetGroup: event.currentTarget.value })} />
              <PasswordInput label={editingId ? 'Hub API 密钥（留空则保留原密钥）' : 'Hub API 密钥'} required={!editingId} autoComplete="new-password" value={form.secret} onChange={(event) => setForm({ ...form, secret: event.currentTarget.value })} />
              <Group justify="flex-end"><Button type="button" variant="default" radius={6} onClick={cancelEdit}>返回</Button><Button type="submit" radius={6} loading={pending === 'save'}>{editingId ? '保存修改' : '保存配置'}</Button></Group>
            </Stack>
          </form>
        </Paper>
      ) : null}

      <Stack gap="md">
        {destinations.map((destination) => (
          <Card key={destination.id} withBorder radius={12} padding="lg">
            <Group justify="space-between" align="flex-start" wrap="nowrap">
              <div><Title order={3} size="h4">{destination.name}</Title><Text size="sm" c="dimmed" mt={4}>{destination.endpoint}</Text><Text size="sm" c="dimmed">目标组 ID：{destination.targetGroup}</Text></div>
              <Badge color={destination.enabled ? 'green' : 'gray'} variant="light">{destination.enabled ? '已启用' : '已停用'}</Badge>
            </Group>
            <DestinationTestSummary destination={destination} />
            <Divider my="md" />
            <Group justify="space-between" align="center">
              <Switch label={destination.enabled ? '启用' : '停用'} checked={destination.enabled} onChange={(event) => void performAction(`toggle:${destination.id}`, () => destinationApi.setEnabled(destination.id, event.currentTarget.checked))} disabled={pending !== null} />
              <Group gap="xs"><Button variant="subtle" radius={6} onClick={() => startEdit(destination)} disabled={pending !== null}>修改</Button><Button variant="light" radius={6} onClick={() => void performAction(`test:${destination.id}`, () => destinationApi.test(destination.id))} loading={pending === `test:${destination.id}`} disabled={pending !== null || !destination.enabled}>测试连接与目标组</Button><Button radius={6} onClick={() => void performAction(`select:${destination.id}`, () => destinationApi.select(destination.id))} disabled={pending !== null || !destination.enabled || destination.test?.connection !== 'connected' || destination.test?.target !== 'connected'}>{selectedId === destination.id ? '当前已选择' : '选择去向'}</Button></Group>
            </Group>
          </Card>
        ))}
      </Stack>
      {selectedId ? <Text size="sm" c="dimmed" mt="md">当前选择只表示后续操作的目标配置，不表示任何客户资料已经发送或交付完成。</Text> : null}
    </section>
  );
}
