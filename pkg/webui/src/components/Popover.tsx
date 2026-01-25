import React, { useEffect, useRef, useState } from 'react';

export interface PopoverItem {
  label: string;
  value: string;
  copyValue?: string; // Full value to copy (if different from display value)
}

export interface PopoverData {
  title: string;
  items: PopoverItem[];
}

interface PopoverProps {
  data: PopoverData;
  x: number;
  y: number;
  onClose: () => void;
}

export const Popover: React.FC<PopoverProps> = ({ data, x, y, onClose }) => {
  const popoverRef = useRef<HTMLDivElement>(null);
  const [copiedIndex, setCopiedIndex] = useState<number | null>(null);

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
      className="event-popover"
      style={{ left: x, top: y }}
      onClick={(e) => e.stopPropagation()}
    >
      <div className="popover-title">{data.title}</div>
      <table className="popover-table">
        <tbody>
          {data.items.map((item, index) => (
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
    </div>
  );
};
