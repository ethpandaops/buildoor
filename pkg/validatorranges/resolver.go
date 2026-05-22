package validatorranges

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"

	"github.com/ethpandaops/buildoor/pkg/config"
)

type rangeEntry struct {
	start uint64
	end   uint64
	name  string
}

// Resolver maps validator indices to client names using configured ranges.
type Resolver struct {
	cfg    *config.ValidatorRangesConfig
	log    logrus.FieldLogger
	ranges []rangeEntry
	mu     sync.RWMutex
}

func NewResolver(cfg *config.ValidatorRangesConfig, log logrus.FieldLogger) *Resolver {
	return &Resolver{cfg: cfg, log: log.WithField("component", "validator-ranges")}
}

// Start loads ranges and, if a URL is configured, refreshes every 5 minutes.
func (r *Resolver) Start(ctx context.Context) {
	if r.cfg.File == "" && r.cfg.URL == "" {
		return
	}
	if err := r.load(ctx); err != nil {
		r.log.WithError(err).Warn("Failed to load validator ranges")
	}
	if r.cfg.URL == "" {
		return
	}
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := r.loadFromURL(ctx); err != nil {
					r.log.WithError(err).Warn("Failed to refresh validator ranges")
				}
			}
		}
	}()
}

// GetClientName returns the client name for the given validator index, or "" if not found.
func (r *Resolver) GetClientName(index phase0.ValidatorIndex) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for i := range r.ranges {
		if uint64(index) >= r.ranges[i].start && uint64(index) <= r.ranges[i].end {
			return r.ranges[i].name
		}
	}
	return ""
}

func (r *Resolver) load(ctx context.Context) error {
	if r.cfg.URL != "" {
		return r.loadFromURL(ctx)
	}
	return r.loadFromFile()
}

func (r *Resolver) loadFromFile() error {
	data, err := os.ReadFile(r.cfg.File)
	if err != nil {
		return fmt.Errorf("read validator ranges file: %w", err)
	}
	var raw map[string]string
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parse validator ranges file: %w", err)
	}
	return r.applyRanges(raw)
}

func (r *Resolver) loadFromURL(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.cfg.URL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetch validator ranges: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}
	var payload struct {
		Ranges map[string]string `json:"ranges"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("parse validator ranges response: %w", err)
	}
	return r.applyRanges(payload.Ranges)
}

func (r *Resolver) applyRanges(raw map[string]string) error {
	entries := make([]rangeEntry, 0, len(raw))
	for k, v := range raw {
		e, err := parseRange(k, v)
		if err != nil {
			r.log.WithField("key", k).WithError(err).Warn("Skipping invalid validator range entry")
			continue
		}
		entries = append(entries, e)
	}
	r.mu.Lock()
	r.ranges = entries
	r.mu.Unlock()
	r.log.WithField("count", len(entries)).Info("Loaded validator ranges")
	return nil
}

func parseRange(key, name string) (rangeEntry, error) {
	parts := strings.SplitN(strings.TrimSpace(key), "-", 2)
	if len(parts) != 2 {
		return rangeEntry{}, fmt.Errorf("expected 'start-end', got %q", key)
	}
	start, err := strconv.ParseUint(strings.TrimSpace(parts[0]), 10, 64)
	if err != nil {
		return rangeEntry{}, fmt.Errorf("invalid start %q: %w", parts[0], err)
	}
	end, err := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 64)
	if err != nil {
		return rangeEntry{}, fmt.Errorf("invalid end %q: %w", parts[1], err)
	}
	return rangeEntry{start: start, end: end, name: strings.TrimSpace(name)}, nil
}
