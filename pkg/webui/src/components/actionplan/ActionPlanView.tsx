import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useEventStream } from '../../hooks/useEventStream';
import { useActionPlan } from '../../hooks/useActionPlan';
import { useActionPlanRules } from '../../hooks/useActionPlanRules';
import { useAuthContext } from '../../context/AuthContext';
import { ActionPlanGrid } from './ActionPlanGrid';
import { BulkEditBar } from './BulkEditBar';
import { RulesPanel } from './RulesPanel';
import { SlotEditDrawer, type ModalTarget } from './SlotEditDrawer';
import type { CellClickModifiers } from './SlotCell';
import { InfoHint } from '../InfoHint';

// Default epoch window around the current epoch.
const EPOCHS_BACK = 8;
const EPOCHS_AHEAD = 4;

interface EpochWindow {
  start: number;
  end: number;
}

export const ActionPlanView: React.FC = () => {
  const { chainInfo, currentSlot } = useEventStream();
  const { isLoggedIn } = useAuthContext();
  const canEdit = isLoggedIn;

  const slotsPerEpoch = chainInfo?.slots_per_epoch ?? 0;
  const currentEpoch = slotsPerEpoch > 0 ? Math.floor(currentSlot / slotsPerEpoch) : 0;

  // The view follows the current epoch by default; navigating (Older/Newer/
  // jump) pins an explicit window until "Now" resumes following.
  const [follow, setFollow] = useState(true);
  const [pinned, setPinned] = useState<EpochWindow | null>(null);
  const [jumpInput, setJumpInput] = useState('');

  const ready = !!chainInfo && slotsPerEpoch > 0 && currentSlot > 0;

  // The effective window: derived live from the current epoch while following
  // (so it advances as epochs pass), otherwise the pinned range.
  const window: EpochWindow | null = !ready
    ? null
    : follow
      ? { start: Math.max(0, currentEpoch - EPOCHS_BACK), end: currentEpoch + EPOCHS_AHEAD }
      : pinned;

  const minSlot = window ? window.start * slotsPerEpoch : 0;
  const maxSlot = window ? (window.end + 1) * slotsPerEpoch - 1 : -1;

  const { plans, results, details, loadDetail, loading, error, refetch, applyUpdates } = useActionPlan(minSlot, maxSlot);
  const {
    rules,
    loading: rulesLoading,
    error: rulesError,
    saveRules,
  } = useActionPlanRules();

  // Selection (future slots only) + range anchor for shift-click.
  const [selection, setSelection] = useState<Set<number>>(new Set());
  const lastClickedRef = useRef<number | null>(null);

  const [drawerTarget, setDrawerTarget] = useState<ModalTarget | null>(null);
  const drawerDirtyRef = useRef(false);
  const handleDirtyChange = useCallback((dirty: boolean) => {
    drawerDirtyRef.current = dirty;
  }, []);

  // Every selection change retargets the open drawer; unsaved edits are only
  // discarded after a confirmation.
  const retarget = useCallback((target: ModalTarget | null): boolean => {
    if (drawerDirtyRef.current && !globalThis.confirm('Discard the unsaved plan changes?')) return false;
    drawerDirtyRef.current = false;
    setDrawerTarget(target);
    return true;
  }, []);

  const selectSlots = useCallback(
    (next: Set<number>) => {
      const slots = [...next].sort((a, b) => a - b);
      if (!retarget(slots.length > 0 ? { slots } : null)) return;
      setSelection(next);
    },
    [retarget]
  );

  // Click selects the slot and opens it, shift-click selects the range from
  // the anchor (the last plain or toggle click), ctrl/cmd-click toggles one
  // slot. Only future slots are selectable; a past slot opens its outcome.
  const handleCellClick = useCallback(
    (slot: number, { shift, toggle }: CellClickModifiers) => {
      const anchor = lastClickedRef.current;
      if (shift && anchor !== null) {
        const next = new Set<number>();
        for (let s = Math.min(anchor, slot); s <= Math.max(anchor, slot); s++) {
          if (s > currentSlot) next.add(s);
        }
        selectSlots(next);
        return;
      }
      lastClickedRef.current = slot;
      if (toggle && slot > currentSlot) {
        const next = new Set([...selection].filter((s) => s > currentSlot));
        if (next.has(slot)) next.delete(slot);
        else next.add(slot);
        selectSlots(next);
        return;
      }
      if (slot > currentSlot) {
        selectSlots(new Set([slot]));
        return;
      }
      if (retarget({ slots: [slot] })) setSelection(new Set());
    },
    [currentSlot, selection, selectSlots, retarget]
  );

  const handleEditSelected = useCallback(() => {
    if (selection.size === 0) return;
    retarget({ slots: [...selection].sort((a, b) => a - b) });
  }, [selection, retarget]);

  const handleEditRange = useCallback(
    (fromSlot: number, toSlot: number) => {
      retarget({ fromSlot, toSlot });
    },
    [retarget]
  );

  const closeDrawer = useCallback(() => {
    if (retarget(null)) setSelection(new Set());
  }, [retarget]);

  // A past slot opens its full outcome record; the grid only holds summaries.
  const detailSlot =
    drawerTarget?.slots?.length === 1 && drawerTarget.slots[0] <= currentSlot ? drawerTarget.slots[0] : null;
  useEffect(() => {
    if (detailSlot !== null) loadDetail(detailSlot);
  }, [detailSlot, loadDetail]);
  const drawerResults = useMemo(() => ({ ...results, ...details }), [results, details]);

  // Shifting pins the window (stops following) so the manual position sticks.
  const shiftWindow = (epochs: number) => {
    if (!window) return;
    const span = window.end - window.start;
    const start = Math.max(0, window.start + epochs);
    setPinned({ start, end: start + span });
    setFollow(false);
  };

  const jumpToCurrent = () => {
    setFollow(true);
  };

  const handleJump = (e: React.FormEvent) => {
    e.preventDefault();
    const epoch = parseInt(jumpInput, 10);
    if (isNaN(epoch) || epoch < 0) return;
    setPinned({ start: Math.max(0, epoch - EPOCHS_BACK), end: epoch + EPOCHS_AHEAD });
    setFollow(false);
  };

  if (!chainInfo || window === null) {
    return (
      <div className="container-fluid mt-2 text-center py-5 text-muted">
        <div className="spinner-border spinner-border-sm me-2" role="status"></div>
        Waiting for chain info...
      </div>
    );
  }

  return (
    <div className={`container-fluid mt-2${drawerTarget ? ' ap-drawer-open' : ''}`}>
      <div className="card">
        <div className="card-header d-flex flex-wrap align-items-center gap-2">
          <h5 className="mb-0">
            Action Plan
            <InfoHint>
              Per-slot instructions on top of the global config: force or suppress p2p bidding,
              Builder API serving and the reveal, override their values, tweak the build. A slot&apos;s
              plan freezes about one slot ahead; from then on (and for past slots) it is read-only
              and the cell shows what actually happened.
            </InfoHint>
          </h5>
          <span className="text-muted small">
            epochs {window.start}–{window.end} (current: {currentEpoch})
            {follow && <span className="badge bg-success ms-2">live</span>}
          </span>
          {loading && <span className="spinner-border spinner-border-sm text-primary"></span>}

          <div className="d-flex flex-wrap align-items-center gap-2 ms-auto">
            <div className="btn-group btn-group-sm">
              <button type="button" className="btn btn-outline-secondary" onClick={() => shiftWindow(-(window.end - window.start + 1))} disabled={window.start === 0} title="Older epochs">
                <i className="fas fa-chevron-left"></i> Older
              </button>
              <button type="button" className={`btn ${follow ? 'btn-primary' : 'btn-outline-secondary'}`} onClick={jumpToCurrent} title="Follow the current epoch">
                Now
              </button>
              <button type="button" className="btn btn-outline-secondary" onClick={() => shiftWindow(window.end - window.start + 1)} title="Newer epochs">
                Newer <i className="fas fa-chevron-right"></i>
              </button>
            </div>
            <form className="d-flex align-items-center gap-1" onSubmit={handleJump}>
              <InfoHint alignEnd>
                Jump to an epoch: shows {EPOCHS_BACK} epochs before it and {EPOCHS_AHEAD} after.
                &quot;Now&quot; resumes following the current epoch.
              </InfoHint>
              <input
                type="number"
                className="form-control form-control-sm ap-range-input"
                placeholder="Epoch"
                min={0}
                value={jumpInput}
                onChange={(e) => setJumpInput(e.target.value)}
              />
              <button type="submit" className="btn btn-sm btn-outline-secondary" disabled={jumpInput === ''}>
                Go
              </button>
            </form>
            <button type="button" className="btn btn-sm btn-outline-secondary" onClick={refetch} title="Refresh">
              <i className="fas fa-rotate"></i>
            </button>
          </div>
        </div>

        <div className="card-body">
          {error && <div className="alert alert-danger small py-2">{error}</div>}

          <RulesPanel
            rules={rules}
            slotsPerEpoch={slotsPerEpoch}
            canEdit={canEdit}
            loading={rulesLoading}
            error={rulesError}
            saveRules={saveRules}
          />

          <BulkEditBar
            selectionCount={selection.size}
            canEdit={canEdit}
            currentSlot={currentSlot}
            onClearSelection={closeDrawer}
            onEditSelected={handleEditSelected}
            onEditRange={handleEditRange}
          />

          <ActionPlanGrid
            startEpoch={window.start}
            endEpoch={window.end}
            slotsPerEpoch={slotsPerEpoch}
            currentSlot={currentSlot}
            plans={plans}
            results={results}
            selection={selection}
            onCellClick={handleCellClick}
          />

          <div className="d-flex flex-wrap align-items-center gap-3 mt-2 timeline-legend">
            <span className="legend-section">
              Plan:
              <InfoHint>
                Chips mark the categories a slot&apos;s plan sets. Blue = custom (forced active,
                optional overrides), red = disabled (suppressed), no chip = inherits the global
                config. Dashed chips come from a recurring rule, not a plan stored for the slot.
              </InfoHint>
            </span>
            <span><span className="ap-chip ap-chip-custom">B</span> bid</span>
            <span><span className="ap-chip ap-chip-custom">A</span> builder api</span>
            <span><span className="ap-chip ap-chip-custom">R</span> reveal</span>
            <span><span className="ap-chip ap-chip-reorg">P</span> reorg parent</span>
            <span><span className="ap-chip ap-chip-transform">jq</span> transform</span>
            <span><span className="ap-chip ap-chip-disabled">IL</span> inclusion list censored</span>
            <span><span className="ap-chip ap-chip-disabled">B</span> disabled</span>
            <span><span className="ap-chip ap-chip-custom ap-chip-rule">B</span> from a recurring rule</span>
            <span className="legend-section">
              Bid (left dot):
              <InfoHint>
                The slot&apos;s outcome, best first: <strong>included</strong> = a block committed to our
                payload; <strong>bid, not included</strong> = p2p bids gossiped or Builder API bids
                served, but another payload won; <strong>failed</strong> = the build failed;{' '}
                <strong>built only</strong> = a payload was built but no bid went out;{' '}
                <strong>nothing happened</strong> = the slot was active but no payload was built
                (attributes missing or the build still running).
              </InfoHint>
            </span>
            <span><span className="ap-dot ap-dot-included d-inline-block"></span> included</span>
            <span><span className="ap-dot ap-dot-bidding d-inline-block"></span> bid, not included</span>
            <span><span className="ap-dot ap-dot-failed d-inline-block"></span> failed</span>
            <span><span className="ap-dot ap-dot-idle d-inline-block"></span> nothing happened</span>
            <span><span className="ap-dot ap-dot-built d-inline-block"></span> built only</span>
            <span className="legend-section">
              Payload (right dot, won slots):
              <InfoHint alignEnd>
                Whether our payload became canonical, decided by the next canonical block and revised
                on reorgs: <strong>canonical</strong> = built upon; <strong>missed</strong> = our block
                stayed but its payload was skipped (withheld, too late or voted empty);{' '}
                <strong>reorged out</strong> = our block itself left the chain;{' '}
                <strong>pending</strong> = no follow-up block yet.
              </InfoHint>
            </span>
            <span><span className="ap-dot ap-dot-payload-canonical d-inline-block"></span> canonical</span>
            <span><span className="ap-dot ap-dot-payload-missed d-inline-block"></span> missed</span>
            <span><span className="ap-dot ap-dot-payload-orphaned d-inline-block"></span> reorged out</span>
            <span><span className="ap-dot ap-dot-payload-pending d-inline-block"></span> pending</span>
            <span className="text-muted">Click a slot to open it; shift-click selects a range, Ctrl/Cmd-click adds or removes a slot.</span>
          </div>
        </div>
      </div>

      {drawerTarget && (
        <SlotEditDrawer
          key={`${drawerTarget.slots?.join(',') ?? ''}:${drawerTarget.fromSlot ?? ''}:${drawerTarget.toSlot ?? ''}`}
          target={drawerTarget}
          plans={plans}
          results={drawerResults}
          currentSlot={currentSlot}
          canEdit={canEdit}
          applyUpdates={applyUpdates}
          onClose={closeDrawer}
          onDirtyChange={handleDirtyChange}
        />
      )}
    </div>
  );
};
