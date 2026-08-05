import React, { useEffect, useRef, useState } from 'react';

export interface PopoverItem {
  label: string;
  value: string;
  copyValue?: string; // Full value to copy (if different from display value)
}

// A popover tab: one variant of the same event (e.g. the payload built for
// each build-parent candidate), with its own rows and artifact.
export interface PopoverTab {
  key: string;
  label: string;
  color?: string;
  items: PopoverItem[];
  artifact?: { url: string; filename: string };
}

export interface PopoverData {
  title: string;
  items: PopoverItem[];
  // Optional tabs rendered above the rows; the active tab replaces items
  // and artifact. The first tab is selected initially.
  tabs?: PopoverTab[];
  // Use the wide popover variant (long values like extra data / content
  // summaries would otherwise line-break).
  wide?: boolean;
  // Recorded artifact behind this event: renders JSON/SSZ download buttons.
  artifact?: { url: string; filename: string };
}

// downloadSSZ fetches an artifact with SSZ content negotiation and triggers a
// browser download.
export async function downloadSSZ(url: string, filename: string): Promise<void> {
  const res = await fetch(url, { headers: { Accept: 'application/octet-stream' } });
  if (!res.ok) {
    throw new Error(`artifact download failed (${res.status})`);
  }
  const blob = await res.blob();
  const objectUrl = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = objectUrl;
  a.download = filename;
  a.click();
  URL.revokeObjectURL(objectUrl);
}

interface PopoverProps {
  data: PopoverData;
  x: number;
  y: number;
  onClose: () => void;
  // Optional custom content rendered below the items table (widens the popover).
  children?: React.ReactNode;
}

export const Popover: React.FC<PopoverProps> = ({ data, x, y, onClose, children }) => {
  const popoverRef = useRef<HTMLDivElement>(null);
  const [copiedIndex, setCopiedIndex] = useState<number | null>(null);
  const [activeTab, setActiveTab] = useState(0);

  const tabs = data.tabs;
  const active = tabs?.[Math.min(activeTab, tabs.length - 1)];
  const items = active?.items ?? data.items;
  const artifact = active?.artifact ?? data.artifact;

  useEffect(() => {
    const handleClickOutside = (e: MouseEvent) => {
      if (popoverRef.current && !popoverRef.current.contains(e.target as Node)) {
        onClose();
      }
    };

    const timeoutId = setTimeout(() => {
      document.addEventListener('click', handleClickOutside);
    }, 100);

    return () => {
      clearTimeout(timeoutId);
      document.removeEventListener('click', handleClickOutside);
    };
  }, [onClose]);

  const handleCopy = async (index: number, value: string) => {
    try {
      await navigator.clipboard.writeText(value);
      setCopiedIndex(index);
      setTimeout(() => setCopiedIndex(null), 1500);
    } catch (err) {
      console.error('Failed to copy:', err);
    }
  };

  return (
    <div
      ref={popoverRef}
      className={`event-popover ${data.wide ? 'event-popover-wide' : ''}`}
      style={{ left: x, top: y }}
      onClick={(e) => e.stopPropagation()}
    >
      <div className="popover-title">{data.title}</div>
      {tabs && tabs.length > 1 && (
        <div className="popover-tabs">
          {tabs.map((tab, index) => (
            <button
              key={tab.key}
              type="button"
              className={`popover-tab${index === Math.min(activeTab, tabs.length - 1) ? ' active' : ''}`}
              onClick={() => setActiveTab(index)}
            >
              {tab.color && (
                <span className="popover-tab-dot" style={{ backgroundColor: tab.color }} />
              )}
              {tab.label}
            </button>
          ))}
        </div>
      )}
      <table className="popover-table">
        <tbody>
          {items.map((item, index) => (
            <tr key={index}>
              <td className="popover-label">{item.label}</td>
              <td className="popover-value">
                <span className="popover-value-text">{item.value}</span>
                {item.copyValue && (
                  <button
                    className="popover-copy-btn"
                    onClick={() => handleCopy(index, item.copyValue!)}
                    title="Copy to clipboard"
                  >
                    {copiedIndex === index ? (
                      <i className="fas fa-check"></i>
                    ) : (
                      <i className="fas fa-copy"></i>
                    )}
                  </button>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {artifact && (
        <div className="popover-artifacts d-flex gap-1">
          <a
            href={artifact.url}
            target="_blank"
            rel="noreferrer"
            className="btn btn-outline-secondary ap-artifact-btn"
          >
            JSON
          </a>
          <button
            type="button"
            className="btn btn-outline-secondary ap-artifact-btn"
            onClick={() => {
              downloadSSZ(artifact.url, artifact.filename)
                .catch((err) => console.error('artifact download failed:', err));
            }}
          >
            SSZ
          </button>
        </div>
      )}
      {children}
    </div>
  );
};
