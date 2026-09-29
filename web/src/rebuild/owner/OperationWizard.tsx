import { Alert, Badge, Button, Checkbox, Group, Pagination, Paper, Radio, Select, Stack, Text, Title } from '@mantine/core';
import { useEffect, useRef, useState } from 'react';
import type { components } from '../../generated/owner';
import { getMotherDiscovery, getSelectedWorkspaceAccess, getSelectedWorkspaceVerification, listAllMotherAccounts, ownerProblem } from './auth';
import { destinationApi, type Destination } from './deliveryDestination';
import { operationDraftApi, type Draft, type DraftChange, type DraftChild } from './operationDraft';
import { expiryRotationApi, type ExpiryPreview } from './expiryRotation';
import { canConfirmRotation, explicitRotationAssignments, rotationStatus } from './rotationPreviewState';
import { canChooseDestination, draftStatus } from './operationWizardState';
import { createOperationWizardRequests } from './operationWizardRequests';
import { standbyApi, type Batch, type Selection } from './standbyBatches';
import { canShowSelectedWorkspaceFacts } from './workspaceVerification';

type Mother = components['schemas']['MotherAccount'];
type Discovery = components['schemas']['MotherDiscovery'];
type RepairTab = 'materials' | 'workspaces' | 'child' | 'standby' | 'destinations';
const stepNames = { mother: '母号', workspace: '目标空间', children: '待用批次与子号', destination: '交付去向', complete: '选择已保存' } as const;

export default function OperationWizard({ onRepair }: { onRepair: (tab: RepairTab) => void }) {
  const [draft, setDraft] = useState<Draft | null>(null);
  const [rotation, setRotation] = useState<ExpiryPreview | null>(null);
  const [rotationChoices, setRotationChoices] = useState<Record<string, string>>({});
  const confirmationKeys = useRef(new Map<string, string>());
  const [mothers, setMothers] = useState<Mother[]>([]);
  const [discovery, setDiscovery] = useState<Discovery | null>(null);
  const [verifiedWorkspace, setVerifiedWorkspace] = useState(false);
  const [workspaceStatus, setWorkspaceStatus] = useState('');
  const requests = useRef(createOperationWizardRequests());
  const [batches, setBatches] = useState<Batch[]>([]);
  const [destinations, setDestinations] = useState<Destination[]>([]);
  const [selection, setSelection] = useState<Selection | null>(null);
  const [selectedChildren, setSelectedChildren] = useState<Set<string>>(new Set());
  const [childDetails, setChildDetails] = useState<Record<string, { identifier: string; materialStatus: 'complete' | 'needs_totp' }>>({});
  const [candidate, setCandidate] = useState('');
  const [childPage, setChildPage] = useState(1);
  const [loading, setLoading] = useState(true);
  const [pending, setPending] = useState(false);
  const [notice, setNotice] = useState('');

  async function reload() {
    setLoading(true);
    try {
      const current = await operationDraftApi.get().catch((error: unknown) => {
        if (ownerProblem(error).status === 404) return null;
        throw error;
      });
      setDraft(current);
      setRotation(null);
      setRotationChoices({});
      if (current?.step === 'complete') {
        const latest = await expiryRotationApi.latest().catch((error: unknown) => {
          if (ownerProblem(error).status === 404) return null;
          throw error;
        });
        if (latest?.draftId === current.id && latest.draftVersion === current.version) setRotation(latest);
      }
      requests.current.invalidateAll();
      setCandidate(''); setVerifiedWorkspace(false); setWorkspaceStatus(''); setSelection(null); setSelectedChildren(new Set()); setChildDetails({});
      if (current?.step === 'mother') setMothers(await listAllMotherAccounts());
      if (current?.step === 'workspace' && current.motherAccountId) setDiscovery(await getMotherDiscovery(current.motherAccountId));
      if (current?.step === 'children') setBatches(await standbyApi.list());
      if (current?.step === 'destination') setDestinations(await destinationApi.list());
      setNotice('');
    } catch (error: unknown) {
      setNotice(ownerProblem(error).status === 401 ? '登录已过期，请重新登录。' : '当前步骤无法读取，请刷新重试。');
    } finally { setLoading(false); }
  }
  useEffect(() => { void reload(); return () => { requests.current.invalidateAll(); }; }, []);
  useEffect(() => {
    if (draft?.step !== 'workspace' || !draft.motherAccountId || !candidate) return;
    const current = requests.current.workspace.begin();
    setVerifiedWorkspace(false); setWorkspaceStatus('正在检查所选空间事实…');
    void Promise.all([getSelectedWorkspaceAccess(candidate, draft.motherAccountId), getSelectedWorkspaceVerification(candidate, draft.motherAccountId)])
      .then(([access, facts]) => {
        if (!requests.current.workspace.isCurrent(current)) return;
        const ready = canShowSelectedWorkspaceFacts(facts, access, Date.now());
        setVerifiedWorkspace(ready);
        setWorkspaceStatus(ready ? `事实已核验（${facts.permission === 'manage' ? '管理权限已记录' : '仅可读取；不能视作可写'}），仍需明确确认空间。` : '事实、凭据或权限待核验。请进入空间管理修复后返回此步。');
      }).catch(() => { if (requests.current.workspace.isCurrent(current)) setWorkspaceStatus('该空间事实无法读取；请在空间管理核验后重试。'); });
    return () => { requests.current.workspace.invalidate(); };
  }, [draft?.step, draft?.motherAccountId, candidate]);

  useEffect(() => {
    if (draft?.step !== 'children' || !candidate || !selection) return;
    const current = requests.current.childDetails.begin();
    void operationDraftApi.batchChildren(candidate, childPage).then((result) => {
      if (!requests.current.childDetails.isCurrent(current)) return;
      const batch = batches.find((item) => item.id === candidate);
      if (result.batchVersion !== batch?.version) { setNotice('批次已变化；请刷新并重新确认精确范围。'); return; }
      const details: Record<string, { identifier: string; materialStatus: 'complete' | 'needs_totp' }> = {};
      for (const item of result.items) {
        if (selection.members.some((member) => member.accountId === item.accountId && member.membershipVersion === item.membershipVersion)) {
          details[item.accountId] = { identifier: item.identifier, materialStatus: item.materialStatus };
        }
      }
      setChildDetails(details);
    }).catch(() => { if (requests.current.childDetails.isCurrent(current)) setNotice('账号名称读取失败；请刷新后重试。'); });
    return () => { requests.current.childDetails.invalidate(); };
  }, [draft?.step, candidate, selection, childPage, batches]);

  async function save(change?: Omit<DraftChange, 'expectedVersion'>) {
    if (pending) return;
    setPending(true); setNotice('');
    try {
      const result = change && draft
        ? await operationDraftApi.change({ ...change, expectedVersion: draft.version })
        : await operationDraftApi.start();
      setDraft(result);
      // Re-read candidate material for exactly the saved step, including after repair.
      await reload();
    } catch (error: unknown) {
      const problem = ownerProblem(error);
      setNotice(problem.code === 'child_material_incomplete' ? '部分子号资料待补；请补齐资料后回到当前步骤重新确认。' : problem.status === 409 ? '资料或草案已变化；未改写已选对象。请刷新，再明确选择。' : problem.status === 403 ? '安全校验失败；请刷新页面。' : '本步未保存；请检查资料并重试。');
    } finally { setPending(false); }
  }
  async function rotationAction(action: 'preview' | 'confirm' | 'revoke') {
    if (pending || !draft) return;
    setPending(true); setNotice('');
    try {
      let next: ExpiryPreview;
      if (action === 'preview') { next = await expiryRotationApi.preview(); setRotationChoices({}); }
      else if (action === 'revoke' && rotation) next = await expiryRotationApi.revoke(rotation);
      else if (action === 'confirm' && rotation && canConfirmRotation(rotation, draft.version, Date.now()) && explicitRotationAssignments(rotation, rotationChoices) && !draftStatus(draft)) {
        let key = confirmationKeys.current.get(rotation.id);
        if (!key) { key = crypto.randomUUID(); confirmationKeys.current.set(rotation.id, key); }
        next = await expiryRotationApi.confirm(rotation, key, explicitRotationAssignments(rotation, rotationChoices)!);
      } else return;
      setRotation(next);
    } catch (error: unknown) {
      const problem=ownerProblem(error);
      setNotice(problem.code === 'pending_write_fence' ? '授权写入围栏未就绪；匹配仅供预览，不会建立可执行授权。' : problem.status === 409 ? '草案或证据已变化，必须重新预览；本次未授权。' : '操作未保存，请刷新后重试。');
    } finally { setPending(false); }
  }
  async function previewBatch(batchId: string) {
    const current = requests.current.preview.begin();
    setCandidate(batchId); setSelection(null); setSelectedChildren(new Set()); setChildDetails({}); setChildPage(1);
    if (!batchId) { setPending(false); return; }
    setPending(true); setNotice('');
    try { const result = await standbyApi.preview('batch', [], '', batchId); if (requests.current.preview.isCurrent(current)) setSelection(result); }
    catch (error: unknown) { if (requests.current.preview.isCurrent(current)) setNotice(ownerProblem(error).status === 409 ? '批次范围已变化，请刷新后重新选择。' : '批次子号无法读取，请刷新重试。'); }
    finally { if (requests.current.preview.isCurrent(current)) setPending(false); }
  }
  function toggleChild(id: string) {
    const next = new Set(selectedChildren);
    if (next.has(id)) next.delete(id); else next.add(id);
    setSelectedChildren(next);
  }
  const step = draft?.step;
  const stale = draft && draftStatus(draft);
  const batch = batches.find((item) => item.id === candidate);
  const destination = destinations.find((item) => item.id === candidate);
  const pageMembers = selection?.members.slice((childPage - 1) * 50, childPage * 50) ?? [];
  const repair = (tab: RepairTab) => onRepair(tab);

  return <section aria-label="开始操作"><Stack gap="lg">
    <div><Text size="xs" c="dimmed" tt="uppercase">仅选择范围 · 不执行平台操作</Text><Title order={2} mt="xs">开始操作</Title></div>
    {notice ? <Alert color="red" role="alert">{notice}</Alert> : null}
    {loading ? <Text role="status">正在读取草案…</Text> : null}
    {!loading && !draft ? <Paper withBorder radius={12} p="xl"><Stack gap="sm"><Text>本轮还没有草案。先保存选择，以便刷新或重启后继续。</Text><Button loading={pending} onClick={() => void save()}>开始选择母号</Button></Stack></Paper> : null}
    {draft && !loading ? <Paper withBorder radius={12} p="xl"><Stack gap="md">
      <Group justify="space-between"><Title order={3} size="h3">{stepNames[draft.step]}</Title><Badge variant="light">第 {(['mother','workspace','children','destination','complete'].indexOf(draft.step) + 1)} 步 / 5</Badge></Group>
      {stale ? <Alert color="yellow">{stale} 冻结的子号或去向不会自动替换。</Alert> : null}
      {step === 'mother' ? <>
        <Text size="sm">选定这次使用的母号；账号可见性不代表已取得空间操作权限。</Text>
        <Radio.Group label="母号" value={candidate} onChange={setCandidate}><Stack gap="xs" mt="sm">{mothers.filter((item) => item.status === 'active').map((item) => <Radio key={item.id} value={item.id} label={item.displayName} description={`${item.loginIdentifier} · ${item.materialStatus === 'complete' ? '资料已保存' : '资料待补'}`} />)}</Stack></Radio.Group>
        {!mothers.length ? <Alert color="yellow">没有母号资料。请补齐后返回本步骤。</Alert> : null}
        <Group justify="space-between"><Button variant="light" onClick={() => repair('materials')}>补齐母号资料</Button><Button disabled={!candidate || pending} loading={pending} onClick={() => void save({ choice: 'mother', motherAccountId: candidate })}>确认母号，下一步</Button></Group>
      </> : null}
      {step === 'workspace' ? <>
        <Text size="sm">请明确确认规范空间 ID；即使仅有一个也不会自动选择。需要先在空间管理核验读取凭据与完整事实；仅可读取不代表可写。</Text>
        <Radio.Group label="目标空间" value={candidate} onChange={setCandidate}><Stack gap="xs" mt="sm">{discovery?.workspaces.map((item) => <Radio key={item.id} value={item.id} disabled={item.accessStatus !== 'readable'} label={`${item.displayName} · ${item.id}`} description={item.accessStatus === 'readable' ? '可读取；管理权限不由本步骤授予' : '不可读取；请更换入口'} />)}</Stack></Radio.Group>
        {!discovery?.workspaces.length ? <Alert color="yellow">还没有当前母号可读取的空间。请先发现并核验空间，再回到这里。</Alert> : null}
        {workspaceStatus ? <Alert color={verifiedWorkspace ? 'green' : 'yellow'} role="status">{workspaceStatus}</Alert> : null}
        <Group justify="space-between"><Button variant="light" onClick={() => repair('workspaces')}>核验 / 补齐空间</Button><Button disabled={!candidate || !verifiedWorkspace || pending} loading={pending} onClick={() => void save({ choice: 'workspace', workspaceId: candidate })}>明确确认空间，下一步</Button></Group>
      </> : null}
      {step === 'children' ? <>
        <Text size="sm">先选来源批次，再确认本轮具体子号。批次成员数不是这个空间的可加入席位数；每个账号是否符合条件仍待单独核验。</Text>
        <Radio.Group label="来源批次" value={candidate} onChange={(value) => void previewBatch(value)}><Stack gap="xs" mt="sm">{batches.map((item) => <Radio key={item.id} value={item.id} label={item.name} description={`${item.memberCount} 个去重子号（不代表目标空间可用席位）`} />)}</Stack></Radio.Group>
        {!batches.length ? <Alert color="yellow">尚无待用批次。可先补子号资料，再建批次并回到本步骤。</Alert> : null}
        {batch && selection ? <Stack gap="xs"><Text size="sm">当前批次精确范围 {selection.count} 个 · 已选 {selectedChildren.size} 个；勾选本轮具体账号（ID）。</Text>
          <Button variant="light" size="xs" onClick={() => setSelectedChildren(new Set(selection.members.map((item) => item.accountId)))}>明确选择整批 {selection.count} 个（资料待补会阻止保存）</Button>
          <div style={{ maxHeight: 320, overflowY: 'auto' }}>{pageMembers.map((item) => <Checkbox mb="xs" key={item.accountId} label={`${childDetails[item.accountId]?.identifier ?? '账号待显示'} · ${item.accountId} · ${childDetails[item.accountId]?.materialStatus === 'needs_totp' ? '资料待补' : childDetails[item.accountId]?.materialStatus === 'complete' ? '资料已保存（非加入资格）' : '状态加载中'}`} disabled={childDetails[item.accountId]?.materialStatus !== 'complete' && !selectedChildren.has(item.accountId)} checked={selectedChildren.has(item.accountId)} onChange={() => toggleChild(item.accountId)} />)}</div>
          <Pagination total={Math.max(1, Math.ceil(selection.members.length / 50))} value={childPage} onChange={setChildPage} /></Stack> : null}
        <Group justify="space-between"><Group><Button variant="light" onClick={() => repair('child')}>补齐子号资料</Button><Button variant="light" onClick={() => repair('standby')}>整理批次</Button></Group><Button disabled={!selection || !batch || !selectedChildren.size || pending || !draft.workspaceCurrent} loading={pending} onClick={() => {
          const children: DraftChild[] = selection!.members.filter((member) => selectedChildren.has(member.accountId));
          void save({ choice: 'children', batchId: batch!.id, batchVersion: batch!.version, children });
        }}>确认 {selectedChildren.size} 个子号，下一步</Button></Group>
      </> : null}
      {step === 'destination' ? <>
        <Text size="sm">选择已经通过连接与目标组测试的 Sub2API 去向。测试成功不等于客户资料已发送。</Text>
        <Radio.Group label="交付去向" value={candidate} onChange={setCandidate}><Stack gap="xs" mt="sm">{destinations.map((item) => <Radio key={item.id} value={item.id} label={`${item.name} · 目标组 ${item.targetGroup}`} description={item.enabled && item.test?.connection === 'connected' && item.test.target === 'connected' && item.test.revision === item.revision ? '两项测试已通过' : '尚不可选；请启用并重新测试'} disabled={!item.enabled || item.test?.connection !== 'connected' || item.test.target !== 'connected' || item.test.revision !== item.revision} />)}</Stack></Radio.Group>
        {!destinations.length ? <Alert color="yellow">还没有交付去向；先保存配置并测试，再回到本步骤。</Alert> : null}
        <Group justify="space-between"><Button variant="light" onClick={() => repair('destinations')}>配置 / 测试去向</Button><Button disabled={!destination || pending || !canChooseDestination(draft)} loading={pending} onClick={() => void save({ choice: 'destination', destinationId: candidate })}>确认去向，查看选择</Button></Group>
      </> : null}
      {step === 'complete' ? <Stack gap="sm"><Alert color={stale ? 'yellow' : 'green'}>{stale || '本轮选择已保存。这里只是范围草案，不是加入资格、席位预览、写入授权或交付完成。'}</Alert>
        <Text size="sm">母号 {draft.motherAccountId} · 修订 {draft.motherRevision}</Text><Text size="sm">空间 {draft.workspaceId} · 核验 #{draft.verificationId}</Text>
        <Text size="sm">批次 {draft.batchId} · 修订 {draft.batchVersion} · 已冻结 {draft.children.length} 个精确子号</Text>
        <Text size="sm">去向 {draft.destinationId} · 修订 {draft.destinationRevision}</Text><Text size="sm">不会邀请、加入、清退、推送或切换生产路由。</Text>
        <Button variant="light" disabled={Boolean(stale) || pending} loading={pending} onClick={() => void rotationAction('preview')}>重新预览到期换批（只读）</Button>
        {rotation ? <Paper withBorder p="md"><Stack gap="sm">
          <Alert color={rotation.status === 'authorized' ? 'green' : 'yellow'} role="status">{rotationStatus(rotation)}</Alert>
          <Text size="sm">核验 #{rotation.verificationId} · 到期 {rotation.activeUntil} · 来源 {rotation.source} · 证据截止 {rotation.expiresAt}</Text>
          <Text size="sm">目标空间 {rotation.workspaceId} · 母号 {rotation.motherAccountId} · 来源批次 {rotation.batchId}（修订 {rotation.batchVersion}） · 交付去向 {rotation.destinationId}（修订 {rotation.destinationRevision}）</Text>
          <Text size="sm">订阅付费 default 配额：{rotation.paidDefaultEntitlement ?? '待核验'}；独立席位类型占用：{Object.entries(rotation.seatTypeCounts).map(([kind, count]) => `${kind} ${count}`).join(' · ') || '待核验'}。付费配额不等于可替换席位数。</Text>
          <Text size="sm">成员快照（{rotation.members.length}）：{rotation.members.join('；') || '无'}</Text>
          <Text size="sm">发出邀请快照（{rotation.invitations.length}）：{rotation.invitations.join('；') || '无'}</Text>
          <Title order={4}>原席位逐项处理</Title>
          {rotation.slots.map((slot) => <Text size="sm" key={slot.platformMemberId}>{slot.identifier} · {slot.platformMemberId} · {slot.seatType || '类型待核验'} · 使用 {slot.usageState}{slot.everUsed ? '（曾使用）' : ''} · 全局保护 {slot.protectionStatus} · {slot.decision} · {slot.reason}</Text>)}
          <Title order={4}>候选与排除原因</Title>
          {rotation.candidates.map((child) => <Text size="sm" key={child.accountId}>{child.identifier} · {child.accountId} · {child.seatType || '类型待核验'} · 使用 {child.usageState}{child.everUsed ? '（曾使用）' : ''} · 全局保护 {child.protectionStatus} · {child.decision} · {child.reason}</Text>)}
          <Text size="xs">预览指纹：{rotation.digest}。缺少邀请的候选仅标记“需邀请”；任何新邀请是独立的明确准备步骤，本阶段不会发出邀请。</Text>
          {rotation.status === 'authorized' ? <Button variant="light" color="red" loading={pending} onClick={() => void rotationAction('revoke')}>撤销授权（尚未执行）</Button> : null}
          {rotation.status === 'authorized' ? <Text size="sm">明确匹配：{rotation.assignments.map((mapping) => `${mapping.platformMemberId} → ${mapping.accountId}`).join('；')} · 授权指纹 {rotation.authorizationDigest}</Text> : null}
          {canConfirmRotation(rotation, draft.version, Date.now()) && !stale ? <Stack gap="xs">
            <Text size="sm">逐席选择替换子号；不会按顺序自动配对。一个候选不能占用多个席位。</Text>
            {rotation.slots.filter((slot) => slot.decision === 'replaceable').map((slot) => <Select key={slot.platformMemberId} label={`${slot.identifier} · ${slot.platformMemberId} · ${slot.seatType}`} placeholder="明确选择此席候选" data={rotation.candidates.filter((child) => child.decision === 'eligible' && child.seatType === slot.seatType).map((child) => ({ value: child.accountId, label: `${child.identifier} · ${child.accountId}`, disabled: Object.entries(rotationChoices).some(([selectedSlot, id]) => selectedSlot !== slot.platformMemberId && id === child.accountId) }))} value={rotationChoices[slot.platformMemberId] || null} onChange={(value) => setRotationChoices((current) => ({ ...current, [slot.platformMemberId]: value || '' }))} />)}
            <Button disabled={!explicitRotationAssignments(rotation, rotationChoices)} loading={pending} onClick={() => void rotationAction('confirm')}>提交逐席匹配核对（写入围栏未就绪时拒绝授权）</Button>
          </Stack> : null}
        </Stack></Paper> : null}</Stack> : null}
      {step !== 'mother' ? <Group gap="xs"><Text size="sm" c="dimmed">返回修改：</Text>{(['mother','workspace','children','destination'] as const).filter((item) => ['mother','workspace','children','destination','complete'].indexOf(item) < ['mother','workspace','children','destination','complete'].indexOf(draft.step)).map((item) => <Button key={item} size="xs" variant="subtle" disabled={pending} onClick={() => void save({ choice: 'back', backTo: item })}>{stepNames[item]}</Button>)}</Group> : null}
    </Stack></Paper> : null}
    <Group><Button variant="subtle" onClick={() => void reload()} disabled={pending}>刷新当前步骤</Button></Group>
  </Stack></section>;
}
