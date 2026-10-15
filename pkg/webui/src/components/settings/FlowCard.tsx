import React, { useState } from 'react';
import { useAuthContext } from '../../context/AuthContext';
import { openSettings } from '../../stores/settingsDrawerStore';
import type { FlowContext, FlowDef } from '../../settings/flows';
import { Tag } from '../ui/Tag';
import { FlowToggleSwitch } from './FlowToggleSwitch';
import { SourceTag } from './SourceTag';
import { lastUIChange, summarySource, summaryValue } from './summary';

interface FlowCardProps {
  flow: FlowDef;
  ctx: FlowContext;
}

function relativeTime(at: number): string {
  const seconds = Math.max(0, Math.round((Date.now() - at) / 1000));
  if (seconds < 60) return 'just now';
  if (seconds < 3600) return `${Math.round(seconds / 60)} min ago`;
  if (seconds < 86400) return `${Math.round(seconds / 3600)} h ago`;
  return new Date(at).toLocaleDateString();
}

// FlowCard shows one flow on the dashboard: its on/off switch, state and
// headline metric, and its configuration read-only. Configure opens the
// settings drawer.
export const FlowCard: React.FC<FlowCardProps> = ({ flow, ctx }) => {
  const { isLoggedIn } = useAuthContext();
  const [toggleError, setToggleError] = useState<string | null>(null);
  const status = flow.status(ctx);
  const changed = lastUIChange(
    flow.tabs.flatMap((tab) => tab.sections.flatMap((section) => section.keys)),
    ctx,
  );

  return (
    <article className="card fc-card" aria-labelledby={`fc-${flow.id}`}>
      <div className="fc-head">
        <h3 className="fc-title" id={`fc-${flow.id}`}>
          {flow.title}
        </h3>
        <FlowToggleSwitch flow={flow} ctx={ctx} onError={setToggleError} />
      </div>
      <div className="fc-status">
        <Tag tone={status.tone}>{status.label}</Tag>
        <span className="fc-metric" title={status.metric}>
          {status.metric}
        </span>
      </div>
      {toggleError && <div className="sd-field-error">{toggleError}</div>}
      <dl className="bd-kv fc-rows">
        {flow.summary.map((row) => {
          const value = summaryValue(row, ctx);
          return (
            <React.Fragment key={row.label}>
              <dt>{row.label}</dt>
              <dd title={value}>
                {value}
                <SourceTag source={summarySource(row, ctx)} />
              </dd>
            </React.Fragment>
          );
        })}
      </dl>
      <div className="fc-foot">
        <button type="button" className="bd-btn bd-btn-sm" onClick={() => openSettings(flow.id)}>
          {isLoggedIn ? 'Configure' : 'All settings'}
        </button>
        {changed > 0 && (
          <span className="fc-changed" title={new Date(changed).toLocaleString()}>
            changed {relativeTime(changed)}
          </span>
        )}
      </div>
    </article>
  );
};
