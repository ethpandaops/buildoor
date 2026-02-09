import React, { Suspense } from 'react';
import { useView } from './stores/viewStore';
import { Header } from './components/Header';

const DashboardPage = React.lazy(() => import('./pages/DashboardPage'));
const ValidatorsPage = React.lazy(() => import('./pages/ValidatorsPage'));
const BidsWonPage = React.lazy(() => import('./pages/BidsWonPage'));
const ApiDocsPage = React.lazy(() => import('./pages/ApiDocsPage'));

export const App: React.FC = () => {
  const currentView = useView();

  return (
    <>
      <Header />
      <main className="container-fluid mt-2 app-main">
        <Suspense fallback={<div className="text-muted text-center py-5">Loading...</div>}>
          {currentView === 'dashboard' && <DashboardPage />}
          {currentView === 'validators' && <ValidatorsPage />}
          {currentView === 'bids-won' && <BidsWonPage />}
          {currentView === 'api-docs' && <ApiDocsPage />}
        </Suspense>
      </main>
    </>
  );
};
