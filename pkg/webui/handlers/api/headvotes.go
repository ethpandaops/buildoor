package api

import (
	"encoding/hex"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/chain"
)

// headVoteRowUnknown is the row name for attesters outside every configured
// validator range; it always sorts last.
const headVoteRowUnknown = "unknown"

// HeadVoteDetailResponse is the per-name vote-arrival heatmap of one slot:
// rows are validator-ranges client names, counts are vote arrivals per
// fixed-width time bucket from the slot start.
type HeadVoteDetailResponse struct {
	Slot         uint64              `json:"slot"`
	Root         string              `json:"root"`
	SlotStartMs  int64               `json:"slot_start_ms"`
	BucketMs     int64               `json:"bucket_ms"`
	BucketCount  int                 `json:"bucket_count"`
	TotalMembers int                 `json:"total_members"`
	Rows         []HeadVoteDetailRow `json:"rows"`
}

// HeadVoteDetailRow is one client name's bucketed vote arrivals.
type HeadVoteDetailRow struct {
	Name          string `json:"name"`
	Members       int    `json:"members"`
	Seen          int    `json:"seen"`
	InBlockUnseen int    `json:"in_block_unseen"`
	Counts        []int  `json:"counts"`
}

// GetHeadVoteDetail godoc
// @Id getHeadVoteDetail
// @Summary Per-name head-vote arrival heatmap for a slot
// @Tags Stats
// @Description Returns the slot's raw single-attestation arrivals grouped by
// @Description validator-ranges client name into fixed-width time buckets
// @Description from the slot start, plus per-name totals and the count of
// @Description attesters that landed on chain without being seen as singles.
// @Description Only slots still retained by the head vote tracker are served.
// @Produce json
// @Param slot path int true "Slot"
// @Param root query string false "Beacon block root (default: the slot's primary root)"
// @Param bucket_ms query int false "Bucket width in ms (default slot_ms/24, clamped to [50, slot_ms])"
// @Success 200 {object} HeadVoteDetailResponse
// @Failure 400 {object} map[string]string "Bad Request"
// @Failure 404 {object} map[string]string "No vote detail retained for this slot"
// @Failure 503 {object} map[string]string "Head vote tracker unavailable"
// @Router /api/buildoor/head-votes/{slot} [get]
func (h *APIHandler) GetHeadVoteDetail(w http.ResponseWriter, r *http.Request) {
	slot, ok := parseArtifactSlot(w, r)
	if !ok {
		return
	}

	if h.chainSvc == nil || h.chainSvc.GetHeadVoteTracker() == nil {
		writeError(w, http.StatusServiceUnavailable, "head vote tracker unavailable")
		return
	}

	var root phase0.Root

	if rootHex := r.URL.Query().Get("root"); rootHex != "" {
		b, err := hex.DecodeString(strings.TrimPrefix(rootHex, "0x"))
		if err != nil || len(b) != 32 {
			writeError(w, http.StatusBadRequest, "invalid root: must be a 32-byte hex string")
			return
		}

		copy(root[:], b)
	}

	detail, ok := h.chainSvc.GetHeadVoteTracker().GetVoteDetail(slot, root)
	if !ok {
		writeError(w, http.StatusNotFound, "no vote detail retained for this slot")
		return
	}

	slotMs := h.chainSvc.GetChainSpec().SecondsPerSlot.Milliseconds()

	bucketMs := slotMs / 24
	if v := r.URL.Query().Get("bucket_ms"); v != "" {
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid bucket_ms: must be a number")
			return
		}

		bucketMs = min(max(parsed, 50), slotMs)
	}

	// The bucket window covers 1.5 slots so next-slot arrivals stay visible;
	// anything later clamps into the last bucket.
	windowMs := slotMs * 3 / 2
	bucketCount := int((windowMs + bucketMs - 1) / bucketMs)

	nameOf := func(_ phase0.ValidatorIndex) string { return "all validators" }
	if h.valRanges != nil {
		nameOf = func(idx phase0.ValidatorIndex) string {
			if name := h.valRanges.GetClientName(idx); name != "" {
				return name
			}

			return headVoteRowUnknown
		}
	}

	writeJSON(w, http.StatusOK, HeadVoteDetailResponse{
		Slot:         uint64(detail.Slot),
		Root:         "0x" + hex.EncodeToString(detail.BlockRoot[:]),
		SlotStartMs:  h.chainSvc.SlotToTime(slot).UnixMilli(),
		BucketMs:     bucketMs,
		BucketCount:  bucketCount,
		TotalMembers: detail.TotalMembers,
		Rows:         buildHeadVoteRows(detail, nameOf, bucketMs, bucketCount),
	})
}

// buildHeadVoteRows groups a slot's per-attester vote detail by client name
// and buckets the arrival offsets. Rows are sorted by name with the unknown
// row last.
func buildHeadVoteRows(
	detail chain.VoteDetail,
	nameOf func(phase0.ValidatorIndex) string,
	bucketMs int64,
	bucketCount int,
) []HeadVoteDetailRow {
	byName := make(map[string]*HeadVoteDetailRow, 8)

	for _, att := range detail.Attesters {
		name := nameOf(att.Index)

		row, ok := byName[name]
		if !ok {
			row = &HeadVoteDetailRow{
				Name:   name,
				Counts: make([]int, bucketCount),
			}
			byName[name] = row
		}

		row.Members++

		switch {
		case att.SeenAtMs >= 0:
			row.Seen++

			bucket := int(int64(att.SeenAtMs) / bucketMs)
			if bucket >= bucketCount {
				bucket = bucketCount - 1
			}

			row.Counts[bucket]++
		case att.InBlock:
			row.InBlockUnseen++
		}
	}

	rows := make([]HeadVoteDetailRow, 0, len(byName))
	for _, row := range byName {
		rows = append(rows, *row)
	}

	sort.Slice(rows, func(i, j int) bool {
		if (rows[i].Name == headVoteRowUnknown) != (rows[j].Name == headVoteRowUnknown) {
			return rows[j].Name == headVoteRowUnknown
		}

		return rows[i].Name < rows[j].Name
	})

	return rows
}
