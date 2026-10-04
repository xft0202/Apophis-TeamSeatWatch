import { Alert, Badge, Button, Checkbox, Group, Pagination, Paper, Radio, Select, Stack, Text, Title } from '@mantine/core';
import { useEffect, useRef, useState } from 'react';
import type { components } from '../generated/owner';
import { getMotherDiscovery, getSelectedWorkspaceAccess, getSelectedWorkspaceVerification, listAllMotherAccounts, ownerProblem } from './auth';
import { destinationApi, type Destination } from './deliveryDestination';
import { operationDraftApi, type Draft, type DraftChange, type DraftChild } from './operationDraft';
import { expiryRotationApi, type ExpiryPreview } from './expiryRotation';
import RotationRemovalPanel from './RotationRemovalPanel';
import { canConfirmRotation, explicitRotationAssignments, rotationCandidateStatus, rotationStatus } from './rotationPreviewState';
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
      // Keep the last confirmed task visible while supplementary labels are
      // reloaded. A failed label lookup must never erase the saved scope.
      if (current?.step !== 'complete') setRotation(null);
      setRotationChoices({});
      if (current?.step === 'complete') {
        let supplementaryNotice = '';
        try {
          const latest = await expiryRotationApi.latest();
          if (latest.draftId === current.id && latest.draftVersion === current.version) setRotation(latest);
          else setRotation(null);
        } catch (error: unknown) {
          const status = ownerProblem(error).status;
          if (status === 401) throw error;
          if (status === 404) setRotation(null);
          else supplementaryNotice = '预览状态暂时无法读取，已保留当前任务。';
        }
        const labels = await Promise.allSettled([
          listAllMotherAccounts(),
          standbyApi.list(),
          destinationApi.list(),
          getMotherDiscovery(current.motherAccountId!),
        ]);
        const unauthorized = labels.find((result) => result.status === 'rejected' && ownerProblem(result.reason).status === 401);
        if (unauthorized?.status === 'rejected') throw unauthorized.reason;
        const [savedMothers, savedBatches, savedDestinations, savedDiscovery] = labels;
        if (savedMothers.status === 'fulfilled') setMothers(savedMothers.value); else supplementaryNotice ||= '母号名称暂时无法读取，已保留当前任务。';
        if (savedBatches.status === 'fulfilled') setBatches(savedBatches.value); else supplementaryNotice ||= '批次名称暂时无法读取，已保留当前任务。';
        if (savedDestinations.status === 'fulfilled') setDestinations(savedDestinations.value); else supplementaryNotice ||= '去向名称暂时无法读取，已保留当前任务。';
        if (savedDiscovery.status === 'fulfilled') setDiscovery(savedDiscovery.value); else supplementaryNotice ||= '空间名称暂时无法读取，已保留当前任务。';
        setNotice(supplementaryNotice);
      }
      requests.current.invalidateAll();
      setCandidate(''); setVerifiedWorkspace(false); setWorkspaceStatus(''); setSelection(null); setSelectedChildren(new Set()); setChildDetails({});
      if (current?.step === 'mother') setMothers(await listAllMotherAccounts());
      if (current?.step === 'workspace' && current.motherAccountId) setDiscovery(await getMotherDiscovery(current.motherAccountId));
      if (current?.step === 'children') setBatches(await standbyApi.list());
      if (current?.step === 'destination') setDestinations(await destinationApi.list());
      if (current?.step !== 'complete') setNotice('');
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
        setWorkspaceStatus(ready ? `空间已核验 · ${facts.permission === 'manage' ? '可管理' : '仅可读取'}` : '空间待核验');
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
      setNotice(problem.status === 409 ? '权限、草案或证据已变化，必须重新预览；本次未授权。' : '操作未保存，请刷新后重试。');
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
    <div><Text size="xs" c="dimmed" tt="uppercase">选择范围 · 核验授权 · 逐席清退</Text><Title order={2} mt="xs">开始操作</Title></div>
    {notice ? <Alert color="red" role="alert">{notice}</Alert> : null}
    {loading ? <Text role="status">正在读取草案…</Text> : null}
    {!loading && !draft ? <Paper withBorder radius={12} p="xl"><Stack gap="sm"><Text>本轮未开始</Text><Button loading={pending} onClick={() => void save()}>开始选择母号</Button></Stack></Paper> : null}
    {draft && !loading ? <Paper withBorder radius={12} p="xl"><Stack gap="md">
      <Group justify="space-between"><Title order={3} size="h3">{stepNames[draft.step]}</Title><Badge variant="light">第 {(['mother','workspace','children','destination','complete'].indexOf(draft.step) + 1)} 步 / 5</Badge></Group>
      {stale ? <Alert color="yellow">{stale} 冻结的子号或去向不会自动替换。</Alert> : null}
      {step === 'mother' ? <>
        <Radio.Group label="母号" value={candidate} onChange={setCandidate}><Stack gap="xs" mt="sm">{mothers.filter((item) => item.status === 'active').map((item) => <Radio key={item.id} value={item.id} label={item.displayName} description={`${item.loginIdentifier} · ${item.materialStatus === 'complete' ? '资料已保存' : '资料待补'}`} />)}</Stack></Radio.Group>
        {!mothers.length ? <Alert color="yellow">没有母号资料。请补齐后返回本步骤。</Alert> : null}
        <Group justify="space-between"><Button variant="light" onClick={() => repair('materials')}>补齐母号资料</Button><Button disabled={!candidate || pending} loading={pending} onClick={() => void save({ choice: 'mother', motherAccountId: candidate })}>确认母号，下一步</Button></Group>
      </> : null}
      {step === 'workspace' ? <>
        <Radio.Group label="目标空间" value={candidate} onChange={setCandidate}><Stack gap="xs" mt="sm">{discovery?.workspaces.map((item) => <Radio key={item.id} value={item.id} disabled={item.accessStatus !== 'readable'} label={item.displayName} description={item.accessStatus === 'readable' ? '可读取' : '权限不足'} />)}</Stack></Radio.Group>
        {!discovery?.workspaces.length ? <Alert color="yellow">还没有当前母号可读取的空间。请先发现并核验空间，再回到这里。</Alert> : null}
        {workspaceStatus ? <Alert color={verifiedWorkspace ? 'green' : 'yellow'} role="status">{workspaceStatus}</Alert> : null}
        <Group justify="space-between"><Button variant="light" onClick={() => repair('workspaces')}>核验 / 补齐空间</Button><Button disabled={!candidate || !verifiedWorkspace || pending} loading={pending} onClick={() => void save({ choice: 'workspace', workspaceId: candidate })}>明确确认空间，下一步</Button></Group>
      </> : null}
      {step === 'children' ? <>
        <Radio.Group label="来源批次" value={candidate} onChange={(value) => void previewBatch(value)}><Stack gap="xs" mt="sm">{batches.map((item) => <Radio key={item.id} value={item.id} label={item.name} description={`${item.memberCount} 个子号`} />)}</Stack></Radio.Group>
        {!batches.length ? <Alert color="yellow">尚无待用批次。可先补子号资料，再建批次并回到本步骤。</Alert> : null}
        {batch && selection ? <Stack gap="xs"><Text size="sm">批次 {selection.count} 个 · 已选 {selectedChildren.size} 个</Text>
          <Button variant="light" size="xs" onClick={() => setSelectedChildren(new Set(selection.members.map((item) => item.accountId)))}>选择整批 {selection.count} 个</Button>
          <div style={{ maxHeight: 320, overflowY: 'auto' }}>{pageMembers.map((item) => <Checkbox mb="xs" key={item.accountId} label={`${childDetails[item.accountId]?.identifier ?? '账号待显示'} · ${childDetails[item.accountId]?.materialStatus === 'needs_totp' ? '资料待补' : childDetails[item.accountId]?.materialStatus === 'complete' ? '资料已保存' : '状态加载中'}`} disabled={childDetails[item.accountId]?.materialStatus !== 'complete' && !selectedChildren.has(item.accountId)} checked={selectedChildren.has(item.accountId)} onChange={() => toggleChild(item.accountId)} />)}</div>
          <Pagination total={Math.max(1, Math.ceil(selection.members.length / 50))} value={childPage} onChange={setChildPage} /></Stack> : null}
        <Group justify="space-between"><Group><Button variant="light" onClick={() => repair('child')}>补齐子号资料</Button><Button variant="light" onClick={() => repair('standby')}>整理批次</Button></Group><Button disabled={!selection || !batch || !selectedChildren.size || pending || !draft.workspaceCurrent} loading={pending} onClick={() => {
          const children: DraftChild[] = selection!.members.filter((member) => selectedChildren.has(member.accountId));
          void save({ choice: 'children', batchId: batch!.id, batchVersion: batch!.version, children });
        }}>确认 {selectedChildren.size} 个子号，下一步</Button></Group>
      </> : null}
      {step === 'destination' ? <>
        <Radio.Group label="交付去向" value={candidate} onChange={setCandidate}><Stack gap="xs" mt="sm">{destinations.map((item) => <Radio key={item.id} value={item.id} label={`${item.name} · 目标组 ${item.targetGroup}`} description={item.enabled && item.test?.connection === 'connected' && item.test.target === 'connected' && item.test.revision === item.revision ? '两项测试已通过' : '尚不可选；请启用并重新测试'} disabled={!item.enabled || item.test?.connection !== 'connected' || item.test.target !== 'connected' || item.test.revision !== item.revision} />)}</Stack></Radio.Group>
        {!destinations.length ? <Alert color="yellow">还没有交付去向；先保存配置并测试，再回到本步骤。</Alert> : null}
        <Group justify="space-between"><Button variant="light" onClick={() => repair('destinations')}>配置 / 测试去向</Button><Button disabled={!destination || pending || !canChooseDestination(draft)} loading={pending} onClick={() => void save({ choice: 'destination', destinationId: candidate })}>确认去向，查看选择</Button></Group>
      </> : null}
      {step === 'complete' ? <Stack gap="sm"><Alert color={stale ? 'yellow' : 'green'}>{stale || '本轮选择已保存 · 待确认'}</Alert>
        <Text size="sm">母号 {mothers.find((item) => item.id === draft.motherAccountId)?.displayName ?? '已选择'}</Text><Text size="sm">空间 {discovery?.workspaces.find((item) => item.id === draft.workspaceId)?.displayName ?? '已选择'}</Text>
        <Text size="sm">批次 {batches.find((item) => item.id === draft.batchId)?.name ?? '已选择'} · 已选 {draft.children.length} 个子号</Text>
        <Text size="sm">去向 {destinations.find((item) => item.id === draft.destinationId)?.name ?? '已选择'}</Text>
        <Button variant="light" disabled={Boolean(stale) || pending} loading={pending} onClick={() => void rotationAction('preview')}>预览到期换批</Button>
        {rotation ? <Paper withBorder p="md"><Stack gap="sm">
          <Alert color={rotation.status === 'authorized' ? 'green' : 'yellow'} role="status">{rotationStatus(rotation)}</Alert>
          <Text size="sm">订阅到期 {new Date(rotation.activeUntil).toLocaleString()} · {rotation.managementPermission === 'manage' ? '可管理' : '权限待核验'}</Text>
          <Text size="sm">付费席位 {rotation.paidDefaultEntitlement ?? '待核验'} · 待换席位 {rotation.slots.filter((slot) => slot.decision === 'replaceable').length}</Text>
          <Text size="sm">成员快照（{rotation.members.length}）：{rotation.members.join('；') || '无'}</Text>
          <Text size="sm">发出邀请快照（{rotation.invitations.length}）：{rotation.invitations.join('；') || '无'}</Text>
          <Title order={4}>原席位逐项处理</Title>
          {rotation.slots.map((slot) => <Text size="sm" key={slot.platformMemberId}>{slot.identifier} · {slot.everUsed ? '曾使用' : '使用待核验'} · {slot.decision === 'replaceable' ? '待换' : '保留'}</Text>)}
          <Title order={4}>候选与排除原因</Title>
          {rotation.candidates.map((child) => <Text size="sm" key={child.accountId}>{child.identifier} · {rotationCandidateStatus(child)}</Text>)}
          {rotation.status === 'authorized' ? <Button variant="light" color="red" loading={pending} onClick={() => void rotationAction('revoke')}>撤销后续授权</Button> : null}
          {rotation.status === 'authorized' ? <Text size="sm">已确认 {rotation.assignments.length} 个席位</Text> : null}
          {canConfirmRotation(rotation, draft.version, Date.now()) && !stale ? <Stack gap="xs">
            {rotation.slots.filter((slot) => slot.decision === 'replaceable').map((slot) => <Select key={slot.platformMemberId} label={slot.identifier} placeholder="明确选择此席候选" data={rotation.candidates.filter((child) => child.decision === 'eligible' && child.seatType === slot.seatType).map((child) => ({ value: child.accountId, label: child.identifier, disabled: Object.entries(rotationChoices).some(([selectedSlot, id]) => selectedSlot !== slot.platformMemberId && id === child.accountId) }))} value={rotationChoices[slot.platformMemberId] || null} onChange={(value) => setRotationChoices((current) => ({ ...current, [slot.platformMemberId]: value || '' }))} />)}
            <Button disabled={!explicitRotationAssignments(rotation, rotationChoices)} loading={pending} onClick={() => void rotationAction('confirm')}>确认本轮席位与候选</Button>
          </Stack> : null}
        </Stack></Paper> : null}</Stack> : null}
      {step !== 'mother' ? <Group gap="xs"><Text size="sm" c="dimmed">返回修改：</Text>{(['mother','workspace','children','destination'] as const).filter((item) => ['mother','workspace','children','destination','complete'].indexOf(item) < ['mother','workspace','children','destination','complete'].indexOf(draft.step)).map((item) => <Button key={item} size="xs" variant="subtle" disabled={pending} onClick={() => void save({ choice: 'back', backTo: item })}>{stepNames[item]}</Button>)}</Group> : null}
    </Stack></Paper> : null}
    <RotationRemovalPanel preview={rotation} />
    <Group><Button variant="subtle" onClick={() => void reload()} disabled={pending}>刷新当前步骤</Button></Group>
  </Stack></section>;
}
