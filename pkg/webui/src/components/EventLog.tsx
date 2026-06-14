import React, { useState, useRef, useMemo, useEffect, useLayoutEffect } from 'react';
import type { LogEvent } from '../types';
import { getEventTypeClass, getEventCategory, formatEventTime, EVENT_CATEGORIES } from '../utils';
import { CopyableHash } from './CopyableHash';

const SCROLLBACK_KEY = 'buildoor.eventlog.scrollback';
const DEFAULT_SCROLLBACK = 1000;
const MAX_SCROLLBACK = 10000;

const ESTIMATED_ROW = 22; // initial height guess for not-yet-measured rows
const OVERSCAN = 50;       // px rendered above/below the viewport
const BOTTOM_THRESHOLD = 24; // px from bottom still counts as "following newest"

// Matches 0x-prefixed hashes, roots and addresses (>= 8 hex chars) so they can
// be rendered as full, click-to-copy values within an otherwise plain message.
const HASH_RE = /0x[0-9a-fA-F]{8,}/g;

// renderMessage splits a log message into plain text and copyable hash spans.
function renderMessage(message: string): React.ReactNode[] {
  const parts: React.ReactNode[] = [];
  let lastIndex = 0;
  let match: RegExpExecArray | null;

  HASH_RE.lastIndex = 0;
  while ((match = HASH_RE.exec(message)) !== null) {
    if (match.index > lastIndex) {
      parts.push(message.slice(lastIndex, match.index));
    }
    parts.push(
      <CopyableHash key={match.index} value={match[0]} full className="event-hash" />
    );
    lastIndex = match.index + match[0].length;
  }

  if (lastIndex < message.length) {
    parts.push(message.slice(lastIndex));
  }

  return parts;
}

function formatLine(event: LogEvent): string {
  return `${formatEventTime(event.timestamp)} [${event.type}] ${event.message}`;
}

// Largest index i with offsets[i] <= y (offsets ascending).
function lastLE(offsets: number[], y: number): number {
  let lo = 0;
  let hi = offsets.length - 1;
  let res = 0;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    if (offsets[mid] <= y) {
      res = mid;
      lo = mid + 1;
    } else {
      hi = mid - 1;
    }
  }
  return res;
}

// Smallest index i with offsets[i] >= y (offsets ascending).
function firstGE(offsets: number[], y: number): number {
  let lo = 0;
  let hi = offsets.length - 1;
  let res = offsets.length - 1;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    if (offsets[mid] >= y) {
      res = mid;
      hi = mid - 1;
    } else {
      lo = mid + 1;
    }
  }
  return res;
}

// CopyRowButton copies the whole log line (time, level and message) to the
// clipboard, with brief visual feedback.
const CopyRowButton: React.FC<{ text: string }> = ({ text }) => {
  const [copied, setCopied] = useState(false);

  const handleClick = () => {
    navigator.clipboard
      .writeText(text)
      .then(() => {
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      })
      .catch((err) => console.error('Failed to copy:', err));
  };

  return (
    <i
      className={`fas ${copied ? 'fa-check text-success' : 'fa-copy'} event-row-copy`}
      title="Copy log line"
      onClick={handleClick}
    />
  );
};

// VirtualLogList renders only the rows near the viewport (± OVERSCAN), keeping
// the off-screen rows accounted for via top/bottom spacers so scrolling and the
// scrollbar behave normally. Rows are chronological (oldest first); new rows are
// appended at the bottom. We follow the newest entry while the user is at the
// bottom, and leave their position untouched while they're scrolled up.
const VirtualLogList: React.FC<{ items: LogEvent[]; resetKey: string }> = ({ items, resetKey }) => {
  const scrollRef = useRef<HTMLDivElement>(null);
  const heights = useRef<Map<number, number>>(new Map());
  const rowEls = useRef<Map<number, HTMLDivElement>>(new Map());
  const atBottomRef = useRef(true);
  const prevItemsRef = useRef<LogEvent[]>([]);
  const prevResetKey = useRef(resetKey);

  const [scrollTop, setScrollTop] = useState(0);
  const [viewportH, setViewportH] = useState(0);
  const [measureV, setMeasureV] = useState(0);

  const { offsets, total, idIndex } = useMemo(() => {
    const offsets = new Array<number>(items.length + 1);
    const idIndex = new Map<number, number>();
    let acc = 0;
    for (let i = 0; i < items.length; i++) {
      offsets[i] = acc;
      idIndex.set(items[i].id, i);
      acc += heights.current.get(items[i].id) ?? ESTIMATED_ROW;
    }
    offsets[items.length] = acc;
    return { offsets, total: acc, idIndex };
    // measureV invalidates the cache after heights are measured.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [items, measureV]);

  const start = items.length ? Math.min(lastLE(offsets, scrollTop - OVERSCAN), items.length - 1) : 0;
  const end = items.length
    ? Math.max(start + 1, Math.min(firstGE(offsets, scrollTop + viewportH + OVERSCAN) + 1, items.length))
    : 0;
  const visible = items.slice(start, end);

  // Track the scroll container's height.
  useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    const update = () => setViewportH(el.clientHeight);
    update();
    const ro = new ResizeObserver(update);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const onScroll = () => {
    const el = scrollRef.current;
    if (!el) return;
    setScrollTop(el.scrollTop);
    atBottomRef.current = el.scrollHeight - el.scrollTop - el.clientHeight <= BOTTOM_THRESHOLD;
  };

  // Reconcile scroll position when the item set changes (append at bottom, and
  // older rows dropped off the top by the scrollback cap).
  useLayoutEffect(() => {
    const el = scrollRef.current;
    if (!el) return;

    if (prevResetKey.current !== resetKey) {
      prevResetKey.current = resetKey;
      prevItemsRef.current = items;
      el.scrollTop = el.scrollHeight; // newest is at the bottom
      atBottomRef.current = true;
      setScrollTop(el.scrollTop);
      return;
    }

    const prev = prevItemsRef.current;
    // Height removed from the top when old rows are dropped by the cap.
    let removedH = 0;
    if (prev.length && items.length) {
      const pos = prev.findIndex((e) => e.id === items[0].id);
      if (pos > 0) {
        for (let i = 0; i < pos; i++) removedH += heights.current.get(prev[i].id) ?? ESTIMATED_ROW;
      }
    }
    prevItemsRef.current = items;

    // Prune cached heights for items that have scrolled out of the buffer.
    if (heights.current.size > items.length * 3 + 50) {
      const live = new Set(items.map((e) => e.id));
      heights.current.forEach((_, id) => {
        if (!live.has(id)) heights.current.delete(id);
      });
    }

    if (atBottomRef.current) {
      el.scrollTop = el.scrollHeight; // follow newest
    } else if (removedH > 0) {
      el.scrollTop = Math.max(0, el.scrollTop - removedH); // keep view stable
    }
    setScrollTop(el.scrollTop);
  }, [items, resetKey]);

  // Measure rendered rows. Cache real heights and, when a row above the viewport
  // changes height, compensate scrollTop so the visible content doesn't jump.
  useLayoutEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    let changed = false;
    let adjust = 0;
    const st = el.scrollTop;

    rowEls.current.forEach((node, id) => {
      const h = node.offsetHeight;
      const prev = heights.current.get(id);
      if (prev !== h) {
        const idx = idIndex.get(id);
        if (idx !== undefined && offsets[idx] < st) adjust += h - (prev ?? ESTIMATED_ROW);
        heights.current.set(id, h);
        changed = true;
      }
    });

    if (atBottomRef.current) {
      if (changed) el.scrollTop = el.scrollHeight;
    } else if (adjust !== 0) {
      el.scrollTop = st + adjust;
      setScrollTop(el.scrollTop);
    }
    if (changed) setMeasureV((v) => v + 1);
  });

  return (
    <div ref={scrollRef} className="events-log" onScroll={onScroll}>
      <div style={{ height: offsets[start] }} />
      {visible.map((event) => (
        <div
          key={event.id}
          ref={(n) => {
            if (n) rowEls.current.set(event.id, n);
            else rowEls.current.delete(event.id);
          }}
          className={`event-item ${getEventTypeClass(event.type)}`}
        >
          <span className="event-time">{formatEventTime(event.timestamp)}</span>
          <span className="event-message">{renderMessage(event.message)}</span>
          <CopyRowButton text={formatLine(event)} />
        </div>
      ))}
      <div style={{ height: Math.max(0, total - offsets[end]) }} />
    </div>
  );
};

interface EventLogProps {
  events: LogEvent[];
  onClear: () => void;
  // Definite pixel height for the panel so the inner list scrolls rather than
  // expanding the page. Computed by the parent from the available layout height.
  height?: number;
}

export const EventLog: React.FC<EventLogProps> = ({ events, onClear, height }) => {
  const [query, setQuery] = useState('');
  const [disabled, setDisabled] = useState<Set<string>>(new Set());
  const [showFilter, setShowFilter] = useState(false);
  const [copiedAll, setCopiedAll] = useState(false);

  // Scrollback limit is persisted; other filters are ephemeral.
  const [scrollback, setScrollback] = useState<number>(() => {
    const v = parseInt(localStorage.getItem(SCROLLBACK_KEY) || '', 10);
    return Number.isFinite(v) && v > 0 ? v : DEFAULT_SCROLLBACK;
  });
  useEffect(() => {
    localStorage.setItem(SCROLLBACK_KEY, String(scrollback));
  }, [scrollback]);

  // `events` is newest-first; keep the newest `scrollback` and reverse to
  // chronological order so new entries append at the bottom.
  const displayed = useMemo(() => {
    const q = query.trim().toLowerCase();
    return events
      .filter((e) => {
        if (disabled.has(getEventCategory(e.type))) return false;
        if (q && !e.message.toLowerCase().includes(q) && !e.type.toLowerCase().includes(q)) {
          return false;
        }
        return true;
      })
      .slice(0, scrollback)
      .reverse();
  }, [events, disabled, query, scrollback]);

  const filterSig = `${query}|${scrollback}|${[...disabled].sort().join(',')}`;

  // Close the filter flyout on Escape or a click/tap outside it.
  const filterRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!showFilter) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setShowFilter(false);
    };
    const onDown = (e: MouseEvent) => {
      if (filterRef.current && !filterRef.current.contains(e.target as Node)) {
        setShowFilter(false);
      }
    };
    window.addEventListener('keydown', onKey);
    document.addEventListener('mousedown', onDown);
    return () => {
      window.removeEventListener('keydown', onKey);
      document.removeEventListener('mousedown', onDown);
    };
  }, [showFilter]);

  const toggleCategory = (key: string) => {
    setDisabled((prev) => {
      const next = new Set(prev);
      if (next.has(key)) {
        next.delete(key);
      } else {
        next.add(key);
      }
      return next;
    });
  };

  const resetFilters = () => {
    setQuery('');
    setDisabled(new Set());
  };

  const copyAll = () => {
    const text = displayed.map(formatLine).join('\n');
    navigator.clipboard
      .writeText(text)
      .then(() => {
        setCopiedAll(true);
        setTimeout(() => setCopiedAll(false), 1500);
      })
      .catch((err) => console.error('Failed to copy:', err));
  };

  const filterActive = query.trim() !== '' || disabled.size > 0;

  return (
    <div
      className="card mb-3 events-log-container"
      style={height ? { height: `${height}px`, flex: '0 0 auto' } : undefined}
    >
      <div className="card-header d-flex justify-content-between align-items-center">
        <h5 className="mb-0">Recent Events</h5>
        <div className="d-flex gap-1">
          <div className="event-filter-anchor" ref={filterRef}>
            <button
              className={`btn btn-sm ${filterActive || showFilter ? 'btn-secondary' : 'btn-outline-secondary'}`}
              onClick={() => setShowFilter((s) => !s)}
              title="Filter log"
            >
              <i className="fas fa-filter" />
            </button>

            {showFilter && (
              <div className="event-filter-flyout">
                <div className="d-flex justify-content-between align-items-center mb-2">
                  <span className="fw-semibold small">Filters</span>
                  <button
                    className="btn btn-sm btn-link p-0 text-decoration-none"
                    onClick={resetFilters}
                    disabled={!filterActive}
                  >
                    Reset
                  </button>
                </div>

                <div className="mb-2">
                  <label className="form-label small text-secondary mb-1">Filter text</label>
                  <input
                    type="text"
                    className="form-control form-control-sm"
                    placeholder="Match message or type…"
                    value={query}
                    onChange={(e) => setQuery(e.target.value)}
                    autoFocus
                  />
                </div>

                <div className="mb-2">
                  <label className="form-label small text-secondary mb-1">Scrollback limit</label>
                  <input
                    type="number"
                    className="form-control form-control-sm"
                    style={{ width: '110px' }}
                    min={10}
                    max={MAX_SCROLLBACK}
                    value={scrollback}
                    onChange={(e) => {
                      const v = parseInt(e.target.value, 10);
                      if (Number.isFinite(v)) setScrollback(Math.min(MAX_SCROLLBACK, Math.max(10, v)));
                    }}
                  />
                  <div className="form-text mt-0">Rows kept (10–{MAX_SCROLLBACK}). Saved.</div>
                </div>

                <label className="form-label small text-secondary mb-1">Event types</label>
                <div className="d-flex flex-column gap-1">
                  {EVENT_CATEGORIES.map((cat) => (
                    <label key={cat.key} className="event-filter-cat small mb-0" title={cat.label}>
                      <input
                        type="checkbox"
                        checked={!disabled.has(cat.key)}
                        onChange={() => toggleCategory(cat.key)}
                      />
                      <span className="event-filter-swatch" style={{ background: cat.color }} />
                      {cat.label}
                    </label>
                  ))}
                </div>
              </div>
            )}
          </div>

          <button
            className="btn btn-sm btn-outline-secondary"
            onClick={copyAll}
            title="Copy whole log"
          >
            <i className={`fas ${copiedAll ? 'fa-check text-success' : 'fa-copy'}`} />
          </button>
          <button className="btn btn-sm btn-outline-secondary" onClick={onClear}>
            Clear
          </button>
        </div>
      </div>

      <div className="card-body p-0 d-flex flex-column" style={{ minHeight: 0 }}>
        <VirtualLogList items={displayed} resetKey={filterSig} />
      </div>
    </div>
  );
};
