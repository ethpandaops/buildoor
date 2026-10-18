import React from 'react';
import type { SettingSource } from '../../settings/types';

// SourceTag marks a setting that does not run on its default: "cli" when a
// flag/env/config value supplies it, "ui" when it was changed in the WebUI.
export const SourceTag: React.FC<{ source?: SettingSource }> = ({ source }) => {
  if (!source) return null;
  const title =
    source.source === 'cli'
      ? 'Set by a command-line flag, environment variable or config file'
      : `Changed in the UI${source.updated_at ? ` (${new Date(source.updated_at).toLocaleString()})` : ''}`;
  return (
    <span className={`bd-src bd-src-${source.source}`} title={title}>
      {source.source}
    </span>
  );
};
