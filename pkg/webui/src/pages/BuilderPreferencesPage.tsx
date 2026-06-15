import React from 'react';
import { useBuilderPreferences } from '../hooks/useBuilderPreferences';
import { BuilderPreferencesList } from '../components/BuilderPreferencesList';

const BuilderPreferencesPage: React.FC = () => {
  const { preferences, loading, error } = useBuilderPreferences();

  return (
    <BuilderPreferencesList preferences={preferences} loading={loading} error={error} />
  );
};

export default BuilderPreferencesPage;
