import React from 'react';
import { useAuthContext } from '../../context/AuthContext';
import { openSettings, type FlowId } from '../../stores/settingsDrawerStore';
import { FLOWS } from '../../settings/flows';
import { SETTING_FIELDS } from '../../settings/fields';
import { formatSetting, readSetting } from '../../settings/values';
import { InfoHint } from '../InfoHint';
import { SourceTag } from './SourceTag';
import { useFlowContext } from './useFlowContext';
import '../../styles/settings.css';

interface FlowSettingsSummaryProps {
  flow: FlowId;
  // Restricts the summary to one drawer tab of the flow.
  tab?: string;
}

// FlowSettingsSummary lists a flow's current settings read-only (every field
// of the tab, with its explanation and source), with a button that opens the
// settings drawer on the same tab.
export const FlowSettingsSummary: React.FC<FlowSettingsSummaryProps> = ({ flow: flowId, tab: tabId }) => {
  const flow = FLOWS[flowId];
  const ctx = useFlowContext();
  const { isLoggedIn } = useAuthContext();
  const tabs = tabId ? flow.tabs.filter((t) => t.id === tabId) : flow.tabs;
  const sources = ctx.config?.setting_sources ?? {};

  return (
    <section className="card fs-card">
      <div className="card-body">
        <div className="d-flex align-items-center gap-2 mb-2">
          <h2 className="bd-card-title">{tabs.length === 1 ? tabs[0].label : flow.title} settings</h2>
          <button
            type="button"
            className="bd-btn bd-btn-sm ms-auto"
            onClick={() => openSettings(flowId, tabs[0]?.id)}
          >
            <i className={`fas ${isLoggedIn ? 'fa-pen' : 'fa-eye'}`} aria-hidden="true"></i>
            {isLoggedIn ? 'Edit' : 'Details'}
          </button>
        </div>
        <div className="fs-sections">
          {tabs.map((tab) =>
            tab.sections.map((section, index) => (
              <div className="fs-section" key={`${tab.id}-${index}`}>
                {section.title && <h3 className="bd-group-title">{section.title}</h3>}
                <dl className="bd-kv">
                  {section.keys.map((key) => (
                    <React.Fragment key={key}>
                      <dt>
                        {SETTING_FIELDS[key].short ?? SETTING_FIELDS[key].label}
                        <InfoHint>{SETTING_FIELDS[key].help}</InfoHint>
                      </dt>
                      <dd title={formatSetting(key, readSetting(key, ctx), false)}>
                        {formatSetting(key, readSetting(key, ctx))}
                        <SourceTag source={sources[key]} />
                      </dd>
                    </React.Fragment>
                  ))}
                </dl>
              </div>
            )),
          )}
        </div>
      </div>
    </section>
  );
};
