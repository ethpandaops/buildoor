import React from 'react';
import { useProposerPreferences } from '../hooks/useProposerPreferences';
import { ProposerPreferencesList } from '../components/ProposerPreferencesList';

const ProposerPreferencesPage: React.FC = () => {
  const { preferences, loading, error } = useProposerPreferences();

  return (
    <ProposerPreferencesList preferences={preferences} loading={loading} error={error} />
  );
};

export default ProposerPreferencesPage;
