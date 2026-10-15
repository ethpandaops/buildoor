import React from 'react';

export interface TabItem<T extends string> {
  id: T;
  label: React.ReactNode;
  // Small trailing content (count badge, dirty dot).
  extra?: React.ReactNode;
}

interface TabsProps<T extends string> {
  tabs: TabItem<T>[];
  active: T;
  onChange: (id: T) => void;
  label: string;
}

export function Tabs<T extends string>({ tabs, active, onChange, label }: TabsProps<T>) {
  return (
    <div className="bd-tabs" role="tablist" aria-label={label}>
      {tabs.map((tab) => (
        <button
          key={tab.id}
          type="button"
          role="tab"
          className="bd-tab"
          aria-selected={tab.id === active}
          onClick={() => onChange(tab.id)}
        >
          {tab.label}
          {tab.extra}
        </button>
      ))}
    </div>
  );
}
