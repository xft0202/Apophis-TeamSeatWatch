import { Alert, Badge, Button, Group, Paper, Radio, Select, Stack, Text, Title } from '@mantine/core';
import { useEffect, useRef, useState } from 'react';
import type { components } from '../../generated/owner';
import { getMotherDiscovery, getMotherPersonalAccess, listAllMotherAccounts, ownerProblem, refreshMotherPersonalAccess, runMotherDiscovery } from './auth';
import { canDiscover, confirmWorkspace, isWorkspaceSelectable } from './workspaceSelection';

type Mother = components['schemas']['MotherAccount'];
type Discovery = components['schemas']['MotherDiscovery'];
type PersonalAccess = components['schemas']['MotherPersonalAccess'];

const accessLabels: Record<PersonalAccess['status'], string> = {
  not_verified: '登录待验证',
  verifying: '正在验证登录',
  ready: '登录已验证',
  invalid_login: '登录资料无效',
  missing_credentials: '资料待补',
  refresh_failed: '登录验证失败',
  unavailable: '登录验证暂不可用',
};

const resultLabels: Record<Discovery['status'], string> = {
  not_verified: '未验证',
  discovering: '正在发现空间',
  discovered: '已发现可见空间',
  empty: '未发现 Team 空间',
  session_expired: '登录已失效',
  missing_credentials: '缺少可用资料',
  discovery_failed: '发现失败',
  permission_denied: '权限不足',
  unavailable: '接入暂不可用',
};

const visibilityLabels = { readable: '可读取 · 管理权限待核验', permission_denied: '权限不足', unknown: '权限待核验' } as const;

export default function WorkspaceDiscoveryView() {
  const [mothers, setMothers] = useState<Mother[]>([]);
  const [motherId, setMotherId] = useState<string | null>(null);
  const [discovery, setDiscovery] = useState<Discovery | null>(null);
  const [access, setAccess] = useState<PersonalAccess | null>(null);
  const [candidate, setCandidate] = useState('');
  const [confirmed, setConfirmed] = useState('');
  const [pending, setPending] = useState(false);
  const [notice, setNotice] = useState('');
  const request = useRef(0);

  useEffect(() => {
    let active = true;
    void listAllMotherAccounts().then((items) => { if (active) setMothers(items); })
      .catch(() => { if (active) setNotice('母号列表暂时无法加载。'); });
    return () => { active = false; request.current++; };
  }, []);

  async function selectMother(value: string | null) {
    const current = ++request.current;
    setMotherId(value);
    setDiscovery(null);
    setAccess(null);
    setCandidate('');
    setConfirmed('');
    setNotice('');
    setPending(false);
    if (!value) return;
    setPending(true);
    try {
      const [accessResult, discoveryResult] = await Promise.all([getMotherPersonalAccess(value), getMotherDiscovery(value)]);
      if (current === request.current) { setAccess(accessResult); setDiscovery(discoveryResult); }
    } catch (error) {
      if (current === request.current) setNotice(ownerProblem(error).status === 401 ? '登录已失效。' : '空间记录暂时无法加载。');
    } finally { if (current === request.current) setPending(false); }
  }

  async function refresh() {
    if (!motherId) return;
    const current = ++request.current;
    setPending(true);
    setNotice('');
    setDiscovery(null);
    setCandidate('');
    setConfirmed('');
    try {
      const result = await refreshMotherPersonalAccess(motherId);
      if (current === request.current) setAccess(result);
    } catch (error) {
      if (current === request.current) {
        const problem = ownerProblem(error);
        setNotice(problem.status === 401 ? '登录已失效。' : problem.code === 'csrf_rejected' ? '验证请求已失效，请刷新页面。' : '登录验证未完成，请重试。');
        setAccess(null);
      }
    } finally { if (current === request.current) setPending(false); }
  }

  async function verify() {
    if (!motherId || !canDiscover(access)) return;
    const current = ++request.current;
    setPending(true);
    setNotice('');
    setCandidate('');
    setConfirmed('');
    try {
      const result = await runMotherDiscovery(motherId);
      if (current === request.current) {
        setDiscovery(result);
        if (result.status === 'session_expired' || result.status === 'missing_credentials') setAccess(null);
      }
    } catch (error) {
      if (current === request.current) {
        const problem = ownerProblem(error);
        setNotice(problem.status === 401 ? '登录已失效。' : problem.code === 'csrf_rejected' ? '验证请求已失效，请刷新页面。' : '验证未完成，请重试。');
        setDiscovery(null);
      }
    } finally { if (current === request.current) setPending(false); }
  }

  const mother = mothers.find((item) => item.id === motherId);
  return (
    <section aria-labelledby="workspace-discovery-heading">
      <Stack gap="xl">
        <div>
          <Text size="xs" tt="uppercase" fw={600} c="dimmed" lts="0.08em">空间管理</Text>
          <Title id="workspace-discovery-heading" order={1} size="h2" mt="md">从母号发现空间</Title>
        </div>
        {notice ? <Alert color="red" role="alert">{notice}</Alert> : null}
        <Paper withBorder radius={12} p={{ base: 'lg', sm: 'xl' }}>
          <Stack gap="md">
            <Select
              label="选择母号"
              placeholder="选择已保存母号"
              searchable
              clearable
              data={mothers.filter((item) => item.status === 'active').map((item) => ({ value: item.id, label: item.loginIdentifier }))}
              value={motherId}
              onChange={(value) => void selectMother(value)}
            />
            {mother && mother.materialStatus !== 'complete' ? <Badge color="yellow">资料待补</Badge> : null}
            {access ? <Badge color={canDiscover(access) ? 'green' : 'yellow'} variant="light">{accessLabels[access.status]}</Badge> : null}
            <Group justify="space-between">
              <Text size="sm" c="dimmed">{motherId ? '未选定目标空间' : '请选择母号'}</Text>
              {canDiscover(access) ? (
                <Button onClick={() => void verify()} loading={pending} disabled={pending}>发现可见空间</Button>
              ) : (
                <Button onClick={() => void refresh()} loading={pending} disabled={!motherId || pending}>验证母号登录</Button>
              )}
            </Group>
          </Stack>
        </Paper>
        {discovery ? (
          <Paper withBorder radius={12} p={{ base: 'lg', sm: 'xl' }}>
            <Stack gap="md">
              <Group justify="space-between"><Title order={2} size="h3">发现结果</Title><Badge color={discovery.status === 'discovered' ? 'green' : 'yellow'} variant="light">{resultLabels[discovery.status]}</Badge></Group>
              {discovery.status === 'discovered' ? (
                <>
                  <Radio.Group label="选择目标空间" value={candidate} onChange={(value) => { setCandidate(value); setConfirmed(''); }}>
                    <Stack gap="sm" mt="sm">
                      {discovery.workspaces.map((workspace) => (
                        <Paper withBorder radius={8} p="md" key={workspace.id}>
                          <Radio value={workspace.id} label={workspace.displayName} description={visibilityLabels[workspace.accessStatus]} disabled={!isWorkspaceSelectable(workspace)} />
                        </Paper>
                      ))}
                    </Stack>
                  </Radio.Group>
                  <Group justify="flex-end"><Button disabled={!candidate || pending} onClick={() => setConfirmed(confirmWorkspace(discovery, candidate) ?? '')}>确认目标空间</Button></Group>
                  {confirmed ? <Alert color="yellow" role="status">已确认 {discovery.workspaces.find((workspace) => workspace.id === confirmed)?.displayName} · 管理权限及空间事实仍待核验</Alert> : null}
                </>
              ) : null}
            </Stack>
          </Paper>
        ) : null}
      </Stack>
    </section>
  );
}
