import { useQuery } from '@tanstack/react-query';
import { Alert, Spin } from 'antd';
import { ownerApi } from './api';
import OwnerShell from './OwnerShell';
import { apiFailure, problem } from './problems';

export default function ExitPoolPage() {
  const pool = useQuery({
    queryKey: ['exit-pool', 'page'],
    refetchInterval: 30_000,
    queryFn: async () => {
      const response = await ownerApi.GET('/api/owner/v1/exit-pool');
      if (response.error || !response.data) throw apiFailure(response.error, response.response.status);
      return response.data;
    },
  });

  const data = pool.data;
  const required = data?.mode === 'proxy_required';
  const empty = required && data?.capacity === 0;

  return (
    <OwnerShell>
      <main className="page" style={{ maxWidth: 860 }}>
        <span className="micro">设置</span>
        <h1 className="page__title">出口池</h1>
        {pool.isLoading ? <Spin /> : null}
        {pool.isError && problem(pool.error).status !== 401 ? (
          <Alert type="error" showIcon message="出口状态暂时无法读取" />
        ) : null}
        {data ? (
          <>
            <Alert
              type={empty ? 'error' : required ? 'warning' : 'info'}
              showIcon
              message={empty ? '平台操作已停止' : required ? '代理必需模式' : '直连模式'}
              description={empty
                ? '当前没有已验证的可用出口。代理端点由部署配置提供，Owner 不能在管理端编辑或读取代理秘密。'
                : `已验证出口 ${data.capacity} 个，可用 ${data.available} 个，在用 ${data.inUse} 个。`}
            />
            <div className="quietnote" style={{ marginTop: 20 }}>
              最近验证：{data.validatedAt ? new Date(data.validatedAt).toLocaleString() : '暂无'}
              {' · '}失败端点数：{data.failureCount ?? 0}
            </div>
            {data.failureClasses && Object.entries(data.failureClasses).length > 0 ? (
              <div className="quietnote" style={{ marginTop: 8 }}>
                失败类别：{Object.entries(data.failureClasses).map(([code, count]) => `${code}：${count}`).join(' · ')}
              </div>
            ) : null}
            <p className="quietnote" style={{ marginTop: 20 }}>
              出站代理、认证材料和协议由部署启动配置管理；本页面只展示脱敏准入状态，不保存、不回显，也不修改代理端点。
            </p>
          </>
        ) : null}
      </main>
    </OwnerShell>
  );
}
