package beacon

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	nethttp "net/http"
	"reflect"
	"strings"

	"github.com/ethpandaops/go-eth2-client/spec/altair"
	"github.com/ethpandaops/go-eth2-client/spec/bellatrix"
	"github.com/ethpandaops/go-eth2-client/spec/capella"
	"github.com/ethpandaops/go-eth2-client/spec/deneb"
	"github.com/ethpandaops/go-eth2-client/spec/electra"
	"github.com/ethpandaops/go-eth2-client/spec/fulu"
	"github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/ethpandaops/go-eth2-client/spec/heze"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	dynssz "github.com/pk910/dynamic-ssz"
	"github.com/pk910/dynamic-ssz/ssztypes"
)

// StateSnapshot is the part of a beacon state buildoor's chain view reads.
// GetStateSnapshot streams the state and keeps only these fields: a full
// state is hundreds of MB on a large network (Hoodi), held twice while
// decoding (the body and the decoded copy), every epoch.
type StateSnapshot struct {
	Version        version.DataVersion
	Slot           phase0.Slot
	FinalizedEpoch phase0.Epoch
	Validators     ValidatorSet
	RANDAOMixes    []phase0.Root

	// Electra+
	DepositBalanceToConsume   phase0.Gwei
	ETH1DepositIndex          uint64
	DepositRequestsStartIndex uint64
	PendingDeposits           []*electra.PendingDeposit

	// Fulu+
	ProposerLookahead []phase0.ValidatorIndex

	// Gloas+
	Builders               []*gloas.Builder
	BuilderPendingPayments []*gloas.BuilderPendingPayment
}

// ValidatorSet is the validator registry reduced to what duties, the
// index→pubkey lookup and the deposit queue model need, one entry per
// validator index.
type ValidatorSet struct {
	Pubkeys          []phase0.BLSPubKey
	EffectiveBalance []phase0.Gwei
	ActivationEpoch  []phase0.Epoch
	ExitEpoch        []phase0.Epoch
}

// Len is the number of validators.
func (v *ValidatorSet) Len() int { return len(v.Pubkeys) }

// stateTypes are the fork-specific state types whose SSZ layout the stream
// is read with.
var stateTypes = map[version.DataVersion]reflect.Type{
	version.DataVersionPhase0:    reflect.TypeOf(phase0.BeaconState{}),
	version.DataVersionAltair:    reflect.TypeOf(altair.BeaconState{}),
	version.DataVersionBellatrix: reflect.TypeOf(bellatrix.BeaconState{}),
	version.DataVersionCapella:   reflect.TypeOf(capella.BeaconState{}),
	version.DataVersionDeneb:     reflect.TypeOf(deneb.BeaconState{}),
	version.DataVersionElectra:   reflect.TypeOf(electra.BeaconState{}),
	version.DataVersionFulu:      reflect.TypeOf(fulu.BeaconState{}),
	version.DataVersionGloas:     reflect.TypeOf(gloas.BeaconState{}),
	version.DataVersionHeze:      reflect.TypeOf(heze.BeaconState{}),
}

// validatorSize is the SSZ size of a phase0 Validator, unchanged since
// genesis: pubkey 48, withdrawal credentials 32, effective balance 8,
// slashed 1, activation eligibility / activation / exit / withdrawable
// epochs 8 each.
const validatorSize = 121

// stateReadBuffer is the stream's read buffer.
const stateReadBuffer = 1 << 20

// GetStateSnapshot streams the beacon state as SSZ and decodes only the
// StateSnapshot fields; everything else is read past without being kept.
func (c *Client) GetStateSnapshot(ctx context.Context, stateID string) (*StateSnapshot, error) {
	url := strings.TrimRight(c.baseURL, "/") + "/eth/v2/debug/beacon/states/" + stateID

	req, err := nethttp.NewRequestWithContext(ctx, nethttp.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("state request: %w", err)
	}

	req.Header.Set("Accept", "application/octet-stream")

	resp, err := nethttp.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch state %s: %w", stateID, err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != nethttp.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))

		return nil, fmt.Errorf("fetch state %s: %s: %s", stateID, resp.Status, strings.TrimSpace(string(body)))
	}

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/octet-stream") {
		return nil, fmt.Errorf("fetch state %s: expected SSZ, got %q", stateID, ct)
	}

	var forkVersion version.DataVersion
	if err := forkVersion.UnmarshalJSON([]byte(`"` + strings.ToLower(resp.Header.Get("Eth-Consensus-Version")) + `"`)); err != nil {
		return nil, fmt.Errorf("fetch state %s: consensus version %q: %w", stateID, resp.Header.Get("Eth-Consensus-Version"), err)
	}

	return readStateSnapshot(bufio.NewReaderSize(resp.Body, stateReadBuffer), forkVersion)
}

// readStateSnapshot walks a state's SSZ stream with the fork's layout: the
// fixed part (static fields and the offsets of the dynamic ones), then the
// dynamic fields in order.
func readStateSnapshot(r io.Reader, forkVersion version.DataVersion) (*StateSnapshot, error) {
	stateType, ok := stateTypes[forkVersion]
	if !ok {
		return nil, fmt.Errorf("no state layout for %s", forkVersion)
	}

	ds := dynssz.GetGlobalDynSsz()

	desc, err := ds.GetTypeCache().GetTypeDescriptor(stateType, nil, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("state layout for %s: %w", forkVersion, err)
	}

	snap := &StateSnapshot{Version: forkVersion}
	fields := desc.ContainerDesc.Fields

	type dynField struct {
		field  *ssztypes.FieldDescriptor
		offset uint32
	}

	dynamic := make([]dynField, 0, 16)

	var pos uint64

	for i := range fields {
		field := &fields[i]

		if field.Type.SszTypeFlags&ssztypes.SszTypeFlagIsDynamic != 0 {
			var buf [4]byte
			if _, err := io.ReadFull(r, buf[:]); err != nil {
				return nil, fmt.Errorf("state %s offset: %w", field.Name, err)
			}

			dynamic = append(dynamic, dynField{field: field, offset: binary.LittleEndian.Uint32(buf[:])})
			pos += 4

			continue
		}

		size := uint64(field.Type.Size) //nolint:gosec // descriptor sizes are non-negative
		if err := snap.readStatic(r, ds, field, size); err != nil {
			return nil, err
		}

		pos += size
	}

	for i, dyn := range dynamic {
		if uint64(dyn.offset) != pos {
			return nil, fmt.Errorf("state %s: offset %d, stream at %d", dyn.field.Name, dyn.offset, pos)
		}

		// The last field runs to the end of the stream.
		length := int64(-1)
		if i+1 < len(dynamic) {
			if dynamic[i+1].offset < dyn.offset {
				return nil, fmt.Errorf("state %s: offsets out of order", dyn.field.Name)
			}

			length = int64(dynamic[i+1].offset - dyn.offset)
		}

		read, err := snap.readDynamic(r, ds, dyn.field, length)
		if err != nil {
			return nil, err
		}

		pos += uint64(read) //nolint:gosec // bytes read are non-negative
	}

	return snap, nil
}

// readStatic decodes a wanted static field or skips it.
func (s *StateSnapshot) readStatic(r io.Reader, ds *dynssz.DynSsz, field *ssztypes.FieldDescriptor, size uint64) error {
	switch field.Name {
	case "Slot", "DepositBalanceToConsume", "ETH1DepositIndex", "DepositRequestsStartIndex":
		var buf [8]byte
		if size != 8 {
			return fmt.Errorf("state %s: unexpected size %d", field.Name, size)
		}

		if _, err := io.ReadFull(r, buf[:]); err != nil {
			return fmt.Errorf("state %s: %w", field.Name, err)
		}

		value := binary.LittleEndian.Uint64(buf[:])

		switch field.Name {
		case "Slot":
			s.Slot = phase0.Slot(value)
		case "DepositBalanceToConsume":
			s.DepositBalanceToConsume = phase0.Gwei(value)
		case "ETH1DepositIndex":
			s.ETH1DepositIndex = value
		case "DepositRequestsStartIndex":
			s.DepositRequestsStartIndex = value
		}

		return nil

	case "FinalizedCheckpoint":
		buf := make([]byte, size)
		if _, err := io.ReadFull(r, buf); err != nil {
			return fmt.Errorf("state %s: %w", field.Name, err)
		}

		checkpoint := new(phase0.Checkpoint)
		if err := ds.UnmarshalSSZ(checkpoint, buf); err != nil {
			return fmt.Errorf("state %s: %w", field.Name, err)
		}

		s.FinalizedEpoch = checkpoint.Epoch

		return nil

	case "RANDAOMixes":
		roots, err := readFixedElements(r, size, 32, func(b []byte) phase0.Root { return phase0.Root(b) })
		if err != nil {
			return fmt.Errorf("state %s: %w", field.Name, err)
		}

		s.RANDAOMixes = roots

		return nil

	case "ProposerLookahead":
		indices, err := readFixedElements(r, size, 8, func(b []byte) phase0.ValidatorIndex {
			return phase0.ValidatorIndex(binary.LittleEndian.Uint64(b))
		})
		if err != nil {
			return fmt.Errorf("state %s: %w", field.Name, err)
		}

		s.ProposerLookahead = indices

		return nil

	case "BuilderPendingPayments":
		payments, err := readContainers[gloas.BuilderPendingPayment](r, ds, field, size)
		if err != nil {
			return err
		}

		s.BuilderPendingPayments = payments

		return nil
	}

	return skip(r, int64(size)) //nolint:gosec // descriptor sizes fit an int64
}

// readDynamic decodes a wanted dynamic field or skips it; length -1 = to the
// end of the stream. Returns the bytes consumed.
func (s *StateSnapshot) readDynamic(r io.Reader, ds *dynssz.DynSsz, field *ssztypes.FieldDescriptor, length int64) (int64, error) {
	if length < 0 {
		switch field.Name {
		case "Validators", "PendingDeposits", "Builders", "BuilderPendingPayments":
			return 0, fmt.Errorf("state %s: wanted field at the end of the stream", field.Name)
		}

		n, err := io.Copy(io.Discard, r)
		if err != nil {
			return n, fmt.Errorf("state %s: %w", field.Name, err)
		}

		return n, nil
	}

	size := uint64(length) //nolint:gosec // checked non-negative above

	var err error

	switch field.Name {
	case "Validators":
		s.Validators, err = readValidators(r, size)
	case "PendingDeposits":
		s.PendingDeposits, err = readContainers[electra.PendingDeposit](r, ds, field, size)
	case "Builders":
		s.Builders, err = readContainers[gloas.Builder](r, ds, field, size)
	case "BuilderPendingPayments":
		s.BuilderPendingPayments, err = readContainers[gloas.BuilderPendingPayment](r, ds, field, size)
	default:
		err = skip(r, length)
	}

	if err != nil {
		return 0, fmt.Errorf("state %s: %w", field.Name, err)
	}

	return length, nil
}

// readValidators reduces the registry to a ValidatorSet record by record,
// never holding the full encoded list.
func readValidators(r io.Reader, size uint64) (ValidatorSet, error) {
	if size%validatorSize != 0 {
		return ValidatorSet{}, fmt.Errorf("size %d is not a multiple of %d", size, validatorSize)
	}

	n := size / validatorSize
	set := ValidatorSet{
		Pubkeys:          make([]phase0.BLSPubKey, n),
		EffectiveBalance: make([]phase0.Gwei, n),
		ActivationEpoch:  make([]phase0.Epoch, n),
		ExitEpoch:        make([]phase0.Epoch, n),
	}

	var buf [validatorSize]byte

	for i := range n {
		if _, err := io.ReadFull(r, buf[:]); err != nil {
			return ValidatorSet{}, err
		}

		copy(set.Pubkeys[i][:], buf[0:48])
		set.EffectiveBalance[i] = phase0.Gwei(binary.LittleEndian.Uint64(buf[80:88]))
		set.ActivationEpoch[i] = phase0.Epoch(binary.LittleEndian.Uint64(buf[97:105]))
		set.ExitEpoch[i] = phase0.Epoch(binary.LittleEndian.Uint64(buf[105:113]))
	}

	return set, nil
}

// readFixedElements decodes a sequence of fixed-size basic elements.
func readFixedElements[T any](r io.Reader, size, elemSize uint64, decode func([]byte) T) ([]T, error) {
	if size%elemSize != 0 {
		return nil, fmt.Errorf("size %d is not a multiple of %d", size, elemSize)
	}

	buf := make([]byte, size)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}

	out := make([]T, size/elemSize)
	for i := range out {
		out[i] = decode(buf[uint64(i)*elemSize : uint64(i+1)*elemSize])
	}

	return out, nil
}

// readContainers decodes a list or vector of static containers element by
// element.
func readContainers[T any](r io.Reader, ds *dynssz.DynSsz, field *ssztypes.FieldDescriptor, size uint64) ([]*T, error) {
	elem := field.Type.ElemDesc
	if elem == nil || elem.Size <= 0 || elem.SszTypeFlags&ssztypes.SszTypeFlagIsDynamic != 0 {
		return nil, fmt.Errorf("state %s: not a sequence of static containers", field.Name)
	}

	elemSize := uint64(elem.Size)
	if size%elemSize != 0 {
		return nil, fmt.Errorf("state %s: size %d is not a multiple of %d", field.Name, size, elemSize)
	}

	buf := make([]byte, elemSize)
	out := make([]*T, 0, size/elemSize)

	for range size / elemSize {
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, fmt.Errorf("state %s: %w", field.Name, err)
		}

		item := new(T)
		if err := ds.UnmarshalSSZ(item, buf); err != nil {
			return nil, fmt.Errorf("state %s: %w", field.Name, err)
		}

		out = append(out, item)
	}

	return out, nil
}

func skip(r io.Reader, n int64) error {
	if _, err := io.CopyN(io.Discard, r, n); err != nil {
		return err
	}

	return nil
}
