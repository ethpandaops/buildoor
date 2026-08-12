import React from 'react';
import { BuilderKeysView } from '../components/BuilderKeysView';
import { useEventStream } from '../hooks/useEventStream';

const BuilderKeysPage: React.FC = () => {
  const { builderKeysAggregate, connectionGeneration } = useEventStream();

  return (
    <BuilderKeysView
      streamKeys={builderKeysAggregate}
      connectionGeneration={connectionGeneration}
    />
  );
};

export default BuilderKeysPage;
