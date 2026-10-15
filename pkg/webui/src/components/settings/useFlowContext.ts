import { useMemo } from 'react';
import { useEventStream } from '../../hooks/useEventStream';
import { useLocalBuildStatus } from '../../hooks/useLocalBuildStatus';
import type { FlowContext } from '../../settings/flows';

// useFlowContext gathers the live state the flow cards, summaries and the
// settings drawer derive their values and status from.
export function useFlowContext(): FlowContext {
  const { config, serviceStatus, stats, builderInfo, builderKeysAggregate, txPoolStats } = useEventStream();
  const { status: localBuild } = useLocalBuildStatus(serviceStatus?.local_build_available);

  return useMemo(
    () => ({ config, serviceStatus, stats, builderInfo, builderKeysAggregate, txPoolStats, localBuild }),
    [config, serviceStatus, stats, builderInfo, builderKeysAggregate, txPoolStats, localBuild],
  );
}
