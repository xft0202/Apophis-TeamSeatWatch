import { Alert, Badge, Button, Group, Loader, Stack, Table, Text, Title } from '@mantine/core';
import { useEffect, useRef, useState } from 'react';
import type { components } from '../../generated/owner';
import { getSelectedWorkspaceVerification, ownerProblem, verifySelectedWorkspace } from './auth';
import { canShowWorkspaceFacts } from './workspaceVerification';

type Verification = components['schemas']['SelectedWorkspaceVerification'];

const statusLabels: Record<Verification['status'], string> = {
  pending: '待核验', verified: '已核验', partial: '部分响应 · 待核验',
  failed: '核验失败 · 待核验', permission_denied: '权限不足 · 待核验', stale: '已过期 · 待核验',
};
const permissionLabels: Record<Verification['permission'], string> = {
  manage: '管理权限已核验', read: '仅可读取', denied: '无权限', unknown: '管理权限待核验',
};

function date(value: string | undefined): string {
  return value ? new Date(value).toLocaleString('zh-CN') : '待核验';
}

export default function SelectedWorkspaceConsole({ workspaceId, motherAccountId, onBack, onNextAction }: {
  workspaceId: string;
  motherAccountId: string;
  onBack: () => void;
  onNextAction: () => void;
}) {
  const [verification, setVerification] = useState<Verification | null>(null);
  const [pending, setPending] = useState(false);
  const [notice, setNotice] = useState('');
  const [now, setNow] = useState(Date.now());
  const request = useRef(0);
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 30_000);
    return () => window.clearInterval(timer);
  }, []);
  useEffect(() => {
    setVerification(null);
    const load = () => {
      const current = ++request.current;
      void getSelectedWorkspaceVerification(workspaceId, motherAccountId)
        .then((result) => { if (current === request.current) { setVerification(result); setNotice(''); } })
        .catch(() => { if (current === request.current) { setVerification(null); setNotice('空间入口已失效，请重新发现空间。'); } });
    };
    load();
    const timer = window.setInterval(load, 60_000);
    return () => { request.current++; window.clearInterval(timer); };
  }, [workspaceId, motherAccountId]);

  async function refresh() {
    const current = ++request.current;
    setPending(true);
    setNotice('');
    try {
      const result = await verifySelectedWorkspace(workspaceId, motherAccountId);
      if (current === request.current) setVerification(result);
    } catch (error) {
      if (current !== request.current) return;
      const problem = ownerProblem(error);
      setVerification(null);
      setNotice(problem.code === 'management_protocol_unavailable'
        ? '平台管理核验协议尚未接入，无法确认订阅、席位或成员关系。'
        : problem.status === 409 || problem.status === 403
          ? '空间入口或权限已变化，请重新发现并选择空间。'
          : '核验失败，空间事实仍待核验。');
    } finally { setPending(false); }
  }

  const verified = canShowWorkspaceFacts(verification, now);
  return (
    <section aria-labelledby="selected-workspace-heading">
      <Stack gap="lg">
        <Group justify="space-between" align="start">
          <div>
            <Text size="xs" c="dimmed">空间事实</Text>
            <Title id="selected-workspace-heading" order={2} size="h3">{verification?.workspaceName ?? '选定空间'}</Title>
          </div>
          <Button variant="subtle" onClick={onBack}>更换入口</Button>
        </Group>
        {notice ? <Alert color="yellow" role="alert">{notice}</Alert> : null}
        {!verification && !notice ? <Loader size="sm" aria-label="读取空间核验状态" /> : null}
        {verification ? (
          <>
            <Group gap="sm">
              <Badge color={verified ? 'green' : 'yellow'}>{verified ? statusLabels.verified : verification.status === 'verified' ? statusLabels.stale : statusLabels[verification.status]}</Badge>
              <Badge color={verified ? 'green' : 'gray'} variant="light">{verified ? permissionLabels.manage : verification.permission === 'denied' ? permissionLabels.denied : '管理权限待核验'}</Badge>
              <Text size="sm" c="dimmed">完整性：{verified ? '完整' : '待核验'}</Text>
            </Group>
            <Text size="sm" c="dimmed">来源：{verification.source ?? '待核验'} · 观测：{date(verification.observedAt)} · 有效至：{date(verification.expiresAt)}</Text>
            <Table striped withTableBorder horizontalSpacing="md" verticalSpacing="sm">
              <Table.Tbody>
                <Table.Tr><Table.Th scope="row">订阅到期</Table.Th><Table.Td>{verified ? date(verification.activeUntil) : '待核验'}</Table.Td></Table.Tr>
                <Table.Tr><Table.Th scope="row">席位容量</Table.Th><Table.Td>{verified ? verification.seatLimit : '待核验'}</Table.Td></Table.Tr>
                <Table.Tr><Table.Th scope="row">成员</Table.Th><Table.Td>{verified ? verification.memberCount : '待核验'}</Table.Td></Table.Tr>
                <Table.Tr><Table.Th scope="row">待处理邀请</Table.Th><Table.Td>{verified ? verification.pendingInviteCount : '待核验'}</Table.Td></Table.Tr>
              </Table.Tbody>
            </Table>
            {verified ? (
              <div>
                <Title order={3} size="h4" mb="sm">本空间成员与邀请</Title>
                <Table striped withTableBorder horizontalSpacing="md" verticalSpacing="sm">
                  <Table.Thead><Table.Tr><Table.Th>身份</Table.Th><Table.Th>账号</Table.Th><Table.Th>状态</Table.Th><Table.Th>角色</Table.Th><Table.Th>子号关系</Table.Th><Table.Th>最近核验</Table.Th></Table.Tr></Table.Thead>
                  <Table.Tbody>
                    {verification.members.map((entry) => (
                      <Table.Tr key={`${entry.kind}:${entry.identifier}`}>
                        <Table.Td>{entry.kind === 'member' ? '成员' : '邀请'}</Table.Td>
                        <Table.Td>{entry.identifier}</Table.Td>
                        <Table.Td>{entry.status}</Table.Td>
                        <Table.Td>{entry.role ?? '未提供'}</Table.Td>
                        <Table.Td>{entry.childAccountId ? entry.kind === 'member' ? '已加入本空间' : '本空间邀请待接受' : '未关联子号'}</Table.Td>
                        <Table.Td><Text size="sm">{date(entry.observedAt)} · {entry.latestVerification === 'verified' ? '完整' : '待核验'}</Text><Text size="xs" c="dimmed">{entry.source} · {permissionLabels[entry.permission]}</Text></Table.Td>
                      </Table.Tr>
                    ))}
                  </Table.Tbody>
                </Table>
                {!verification.members.length ? <Text c="dimmed" size="sm" mt="sm">无成员或邀请记录</Text> : null}
              </div>
            ) : <Alert color="yellow">当前无可用管理事实；请核验权限或更换可管理的母号入口。</Alert>}
            <Group justify="flex-end">
              {verification.accessStatus === 'readable' ? <Button variant="light" loading={pending} onClick={() => void refresh()}>核验空间事实</Button> : null}
              {verified ? <Button onClick={onNextAction}>继续处理子号资料</Button> : null}
            </Group>
          </>
        ) : null}
      </Stack>
    </section>
  );
}
