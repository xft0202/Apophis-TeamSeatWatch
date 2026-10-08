import { Box, Button, Group, Select } from '@mantine/core';
import type { MotherWorkspaceScope } from './useMotherWorkspaceScope';
export default function MotherWorkspaceControls({ scope, disabled = false, onChange }: { scope: MotherWorkspaceScope; disabled?: boolean; onChange?: () => void }) {
  return <Group className="scope-controls" gap={16} align="end">
    <Box className="scope-select"><Select label="母号" placeholder="搜索并选择母号" searchable clearable searchValue={scope.search} onSearchChange={scope.searchMothers} onDropdownOpen={() => scope.searchMothers('')} filter={({ options }) => options} data={scope.motherOptions} value={scope.motherId} disabled={disabled || scope.scopeLoading} nothingFoundMessage={scope.optionsLoading ? '正在读取母号' : '没有匹配的母号'} onChange={(value) => { onChange?.(); void scope.chooseMother(value); }} />{scope.hasMore ? <Button size="xs" variant="subtle" disabled={disabled || scope.optionsLoading} onClick={scope.moreMothers}>加载更多母号</Button> : null}</Box>
    <Box className="scope-select"><Select label="空间" placeholder={scope.motherId ? '选择此母号下的空间' : '先选择母号'} searchable clearable data={scope.workspaceOptions} value={scope.workspaceId} disabled={disabled || scope.scopeLoading || !scope.motherId} nothingFoundMessage="此母号没有可读取的空间" onChange={(value) => { onChange?.(); scope.chooseWorkspace(value); }} /></Box>
  </Group>;
}
