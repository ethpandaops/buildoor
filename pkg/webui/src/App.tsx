import React, { Suspense } from 'react';
import { useView } from './stores/viewStore';
import { Header } from './components/Header';
import { SettingsDrawer } from './components/settings/SettingsDrawer';

const DashboardPage = React.lazy(() => import('./pages/DashboardPage'));
const ActionPlanPage = React.lazy(() => import('./pages/ActionPlanPage'));
const BidsWonPage = React.lazy(() => import('./pages/BidsWonPage'));
const MempoolPage = React.lazy(() => import('./pages/MempoolPage'));
const BuilderKeysPage = React.lazy(() => import('./pages/BuilderKeysPage'));
const RegistrationsPage = React.lazy(() => import('./pages/RegistrationsPage'));
const OverviewPage = React.lazy(() => import('./pages/OverviewPage'));
const AuditLogPage = React.lazy(() => import('./pages/AuditLogPage'));
const ApiDocsPage = React.lazy(() => import('./pages/ApiDocsPage'));
const DebugLoginPage = React.lazy(() => import('./pages/DebugLoginPage'));

export const App: React.FC = () => {
  const currentView = useView();

  return (
    <>
      <Header />
      <main className="container-fluid mt-2 app-main">
        <Suspense fallback={<div className="text-muted text-center py-5">Loading...</div>}>
          {currentView === 'dashboard' && <DashboardPage />}
          {currentView === 'action-plan' && <ActionPlanPage />}
          {currentView === 'bids-won' && <BidsWonPage />}
          {currentView === 'mempool' && <MempoolPage />}
          {currentView === 'builder-keys' && <BuilderKeysPage />}
          {currentView === 'registrations' && <RegistrationsPage />}
          {currentView === 'overview' && <OverviewPage />}
          {currentView === 'audit-log' && <AuditLogPage />}
          {currentView === 'api-docs' && <ApiDocsPage />}
          {currentView === 'debug-login' && <DebugLoginPage />}
        </Suspense>
      </main>
      <SettingsDrawer />
    </>
  );
};
