import { createRoot } from 'react-dom/client';
import { QueryClient, QueryClientProvider, useQuery } from '@tanstack/react-query';
import { App, ConfigProvider, Spin } from 'antd';
import { lazy, Suspense, useEffect, type ReactNode } from 'react';
import {
  BrowserRouter,
  Navigate,
  Route,
  Routes,
  useLocation,
  useNavigate,
} from 'react-router';
import { Appearance } from '../appearance';
import { inkLedgerTheme } from './theme';
import { apiFailure, problem } from './problems';
import { ownerApi } from './api';
import '@fontsource/noto-serif-sc/400.css';
import '@fontsource/noto-serif-sc/500.css';
import '@fontsource/jetbrains-mono/400.css';
import '@fontsource/jetbrains-mono/500.css';
import './style.css';
import './ink.css';

const LoginPage = lazy(() => import('./LoginPage'));
const WorkbenchPage = lazy(() => import('./WorkbenchPage'));
const RecordsPage = lazy(() => import('./RecordsPage'));
const AccountsPage = lazy(() => import('./AccountsPage'));
const DeliveryPage = lazy(() => import('./DeliveryPage'));
const ExitPoolPage = lazy(() => import('./ExitPoolPage'));

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: false } },
});

function RouteLoading() {
  return <div className="settings-loading"><Spin size="large" /></div>;
}

function AuthGate({ children }: { children: ReactNode }) {
  const location = useLocation();
  const navigate = useNavigate();
  const loginRoute = location.pathname === '/login';
  const auth = useQuery({
    queryKey: ['auth-status'],
    enabled: !loginRoute,
    queryFn: async () => {
      const response = await ownerApi.GET('/api/owner/v1/auth-status');
      if (response.error || !response.data) throw apiFailure(response.error, response.response.status);
      return response.data;
    },
  });

  useEffect(() => {
    if (problem(auth.error).status !== 401) return;
    navigate('/login', { replace: true, state: { sessionExpired: true } });
  }, [auth.error, navigate]);

  if (loginRoute) return <>{children}</>;
  if (auth.isPending || problem(auth.error).status === 401) return <RouteLoading />;
  if (auth.isError) {
    return <div className="settings-loading">登录状态暂时无法确认，请刷新重试。</div>;
  }
  return <>{children}</>;
}

const root = document.getElementById('root');
if (!root) throw new Error('Owner root element is missing');
createRoot(root).render(
  <Appearance>
    <ConfigProvider theme={inkLedgerTheme} button={{ autoInsertSpace: false }}>
      <App>
      <QueryClientProvider client={queryClient}>
        <BrowserRouter basename="/owner">
          <Suspense fallback={<RouteLoading />}>
            <AuthGate>
              <Routes>
                <Route path="/" element={<WorkbenchPage />} />
                <Route path="/login" element={<LoginPage />} />
                <Route path="/accounts" element={<AccountsPage />} />
                <Route path="/delivery" element={<DeliveryPage />} />
                <Route path="/records" element={<RecordsPage />} />
                <Route path="/exitpool" element={<ExitPoolPage />} />
                <Route path="*" element={<Navigate to="/" replace />} />
              </Routes>
            </AuthGate>
          </Suspense>
        </BrowserRouter>
      </QueryClientProvider>
      </App>
    </ConfigProvider>
  </Appearance>,
);
