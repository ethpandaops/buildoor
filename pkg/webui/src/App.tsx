import React, { Suspense } from 'react';
import { useView } from './stores/viewStore';
import { Header } from './components/Header';

const DashboardPage = React.lazy(() => import('./pages/DashboardPage'));
const ActionPlanPage = React.lazy(() => import('./pages/ActionPlanPage'));
const ValidatorsPage = React.lazy(() => import('./pages/ValidatorsPage'));
const BidsWonPage = React.lazy(() => import('./pages/BidsWonPage'));
const ProposerPreferencesPage = React.lazy(() => import('./pages/ProposerPreferencesPage'));
const BuilderPreferencesPage = React.lazy(() => import('./pages/BuilderPreferencesPage'));
const AuditLogPage = React.lazy(() => import('./pages/AuditLogPage'));
const ApiDocsPage = React.lazy(() => import('./pages/ApiDocsPage'));

export const App: React.FC = () => {
  const currentView = useView();

  return (
    <>
      <Header />
      <main className="container-fluid mt-2 app-main">
        <Suspense fallback={<div className="text-muted text-center py-5">Loading...</div>}>
          {currentView === 'dashboard' && <DashboardPage />}
          {currentView === 'action-plan' && <ActionPlanPage />}
          {currentView === 'validators' && <ValidatorsPage />}
          {currentView === 'bids-won' && <BidsWonPage />}
          {currentView === 'proposer-preferences' && <ProposerPreferencesPage />}
          {currentView === 'builder-preferences' && <BuilderPreferencesPage />}
          {currentView === 'audit-log' && <AuditLogPage />}
          {currentView === 'api-docs' && <ApiDocsPage />}
        </Suspense>
      </main>
    </>
  );
};
