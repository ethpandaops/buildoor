package chain

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/attestantio/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

const (
	seedSize           = int8(32)
	roundSize          = int8(1)
	positionWindowSize = int8(4)
	pivotViewSize      = seedSize + roundSize
	totalSize          = seedSize + roundSize + positionWindowSize

	// EtherGweiFactor is the number of Gwei in 1 ETH.
	EtherGweiFactor = phase0.Gwei(1_000_000_000)
)

var maxShuffleListSize = uint64(1 << 40)

// ActiveIndiceIndex is a uint32 index into the active validators array.
type ActiveIndiceIndex uint32

// DutyState holds the state data needed for duty calculations.
type DutyState struct {
	RandaoMix           *phase0.Hash32
	NextRandaoMix       *phase0.Hash32
	GetRandaoMixes      func() []phase0.Root
	GetActiveCount      func() uint64
	GetEffectiveBalance func(index ActiveIndiceIndex) phase0.Gwei
}

func dutyHash(data []byte) phase0.Hash32 {
	return sha256.Sum256(data)
}

func uintToBytes(data any) []byte {
	switch d := data.(type) {
	case uint64:
		res := make([]byte, 8)
		binary.LittleEndian.PutUint64(res, d)

		return res
	case uint32:
		res := make([]byte, 4)
		binary.LittleEndian.PutUint32(res, d)

		return res
	case uint16:
		res := make([]byte, 2)
		binary.LittleEndian.PutUint16(res, d)

		return res
	case uint8:
		return []byte{d}
	default:
		return nil
	}
}

func bytesToUint(data []byte) uint64 {
	switch len(data) {
	case 1:
		return uint64(data[0])
	case 2:
		return uint64(binary.LittleEndian.Uint16(data))
	case 4:
		return uint64(binary.LittleEndian.Uint32(data))
	case 8:
		return binary.LittleEndian.Uint64(data)
	default:
		return 0
	}
}

func splitOffset(listSize, chunks, index uint64) uint64 {
	return (listSize * index) / chunks
}

// getRandaoMix returns the RANDAO mix for the given epoch from state.
func getRandaoMix(spec *beacon.ChainSpec, state *DutyState, epoch phase0.Epoch) phase0.Hash32 {
	randaoMixes := state.GetRandaoMixes()
	index := int(epoch % phase0.Epoch(spec.EpochsPerHistoricalVector))

	return phase0.Hash32(randaoMixes[index])
}

// getSeed computes the seed for the given epoch and domain type.
func getSeed(
	spec *beacon.ChainSpec,
	state *DutyState,
	epoch phase0.Epoch,
	domainType phase0.DomainType,
) phase0.Hash32 {
	var mix phase0.Hash32
	if state.RandaoMix == nil {
		mix = getRandaoMix(
			spec, state,
			epoch+phase0.Epoch(spec.EpochsPerHistoricalVector-spec.MinSeedLookahead-1),
		)
		state.RandaoMix = &mix

		nextMix := getRandaoMix(
			spec, state,
			epoch+phase0.Epoch(spec.EpochsPerHistoricalVector-spec.MinSeedLookahead-1)+1,
		)
		state.NextRandaoMix = &nextMix
	} else {
		mix = *state.RandaoMix
	}

	data := make([]byte, 0, 4+8+32)
	data = append(data, domainType[:]...)
	data = append(data, uintToBytes(uint64(epoch))...)
	data = append(data, mix[:]...)

	return dutyHash(data)
}

// getAttesterDuties computes attester committee assignments for all slots in an epoch.
func getAttesterDuties(
	spec *beacon.ChainSpec,
	state *DutyState,
	epoch phase0.Epoch,
) ([][][]ActiveIndiceIndex, error) {
	seed := getSeed(spec, state, epoch, spec.DomainBeaconAttester)

	validatorCount := state.GetActiveCount()
	committeesPerSlot := slotCommitteeCount(spec, validatorCount)
	committeesCount := committeesPerSlot * spec.SlotsPerEpoch

	shuffledIndices := make([]ActiveIndiceIndex, validatorCount)
	for i := uint64(0); i < validatorCount; i++ {
		shuffledIndices[i] = ActiveIndiceIndex(i)
	}

	_, err := unshuffleList(spec, shuffledIndices, seed)
	if err != nil {
		return nil, err
	}

	attesterDuties := make([][][]ActiveIndiceIndex, spec.SlotsPerEpoch)

	for slotIndex := uint64(0); slotIndex < spec.SlotsPerEpoch; slotIndex++ {
		committees := make([][]ActiveIndiceIndex, 0, committeesPerSlot)

		for committeeIndex := uint64(0); committeeIndex < committeesPerSlot; committeeIndex++ {
			indexOffset := committeeIndex + (slotIndex * committeesPerSlot)

			start := splitOffset(validatorCount, committeesCount, indexOffset)
			end := splitOffset(validatorCount, committeesCount, indexOffset+1)

			if start > validatorCount || end > validatorCount {
				return nil, errors.New("index out of range")
			}

			committees = append(committees, shuffledIndices[start:end])
		}

		attesterDuties[slotIndex] = committees
	}

	return attesterDuties, nil
}

// slotCommitteeCount returns the number of committees per slot.
func slotCommitteeCount(spec *beacon.ChainSpec, activeValidatorCount uint64) uint64 {
	committeesPerSlot := activeValidatorCount / spec.SlotsPerEpoch / spec.TargetCommitteeSize

	if committeesPerSlot > spec.MaxCommitteesPerSlot {
		return spec.MaxCommitteesPerSlot
	}

	if committeesPerSlot == 0 {
		return 1
	}

	return committeesPerSlot
}

// unshuffleList un-shuffles the list by running backwards through the round count.
func unshuffleList(
	spec *beacon.ChainSpec,
	input []ActiveIndiceIndex,
	seed [32]byte,
) ([]ActiveIndiceIndex, error) {
	return innerShuffleList(spec, input, seed, false)
}

// innerShuffleList shuffles or unshuffles, shuffle=false to un-shuffle.
func innerShuffleList(
	spec *beacon.ChainSpec,
	input []ActiveIndiceIndex,
	seed [32]byte,
	shuffle bool,
) ([]ActiveIndiceIndex, error) {
	if len(input) <= 1 {
		return input, nil
	}

	if uint64(len(input)) > maxShuffleListSize {
		return nil, fmt.Errorf("list size %d out of bounds", len(input))
	}

	rounds := uint8(spec.ShuffleRoundCount)
	if rounds == 0 {
		return input, nil
	}

	listSize := uint64(len(input))
	buf := make([]byte, totalSize)

	r := uint8(0)
	if !shuffle {
		r = rounds - 1
	}

	copy(buf[:seedSize], seed[:])

	for {
		buf[seedSize] = r
		ph := dutyHash(buf[:pivotViewSize])
		pivot := binary.LittleEndian.Uint64(ph[:8]) % listSize
		mirror := (pivot + 1) >> 1

		binary.LittleEndian.PutUint32(buf[pivotViewSize:], uint32(pivot>>8))
		source := dutyHash(buf)
		byteV := source[(pivot&0xff)>>3]

		for i, j := uint64(0), pivot; i < mirror; i, j = i+1, j-1 {
			byteV, source = swapOrNot(buf, byteV, ActiveIndiceIndex(i), input, ActiveIndiceIndex(j), source)
		}

		mirror = (pivot + listSize + 1) >> 1
		end := listSize - 1

		binary.LittleEndian.PutUint32(buf[pivotViewSize:], uint32(end>>8))
		source = dutyHash(buf)
		byteV = source[(end&0xff)>>3]

		for i, j := pivot+1, end; i < mirror; i, j = i+1, j-1 {
			byteV, source = swapOrNot(buf, byteV, ActiveIndiceIndex(i), input, ActiveIndiceIndex(j), source)
		}

		if shuffle {
			r++
			if r == rounds {
				break
			}
		} else {
			if r == 0 {
				break
			}

			r--
		}
	}

	return input, nil
}

// swapOrNot performs a conditional swap in the shuffle algorithm.
func swapOrNot(
	buf []byte,
	byteV byte,
	i ActiveIndiceIndex,
	input []ActiveIndiceIndex,
	j ActiveIndiceIndex,
	source [32]byte,
) (byte, [32]byte) {
	if j&0xff == 0xff {
		binary.LittleEndian.PutUint32(buf[pivotViewSize:], uint32(j>>8))
		source = dutyHash(buf)
	}

	if j&0x7 == 0x7 {
		byteV = source[(j&0xff)>>3]
	}

	bitV := (byteV >> (j & 0x7)) & 0x1

	if bitV == 1 {
		input[i], input[j] = input[j], input[i]
	}

	return byteV, source
}

// getPtcDuties returns the Payload Timeliness Committee (PTC) members for a given slot.
// The PTC is selected from the concatenated attestation committees using balance-weighted selection.
func getPtcDuties(
	spec *beacon.ChainSpec,
	state *DutyState,
	attesterDuties [][]ActiveIndiceIndex,
	slot phase0.Slot,
) ([]ActiveIndiceIndex, error) {
	if spec.PtcSize == 0 {
		return nil, nil
	}

	epoch := phase0.Epoch(uint64(slot) / spec.SlotsPerEpoch)

	seedData := make([]byte, 0, 32+8)
	seedHash := getSeed(spec, state, epoch, spec.DomainPtcAttester)
	seedData = append(seedData, seedHash[:]...)
	seedData = append(seedData, uintToBytes(uint64(slot))...)

	seed := dutyHash(seedData)

	indices := make([]ActiveIndiceIndex, 0)
	for _, committee := range attesterDuties {
		indices = append(indices, committee...)
	}

	if len(indices) == 0 {
		return nil, errors.New("empty committee indices")
	}

	maxRandomValue := uint64(1<<16 - 1)
	total := uint64(len(indices))
	selected := make([]ActiveIndiceIndex, 0, spec.PtcSize)

	for i := uint64(0); uint64(len(selected)) < spec.PtcSize; i++ {
		nextIndex := i % total
		candidateIndex := indices[nextIndex]

		b := make([]byte, 0, 32+8)
		b = append(b, seed[:]...)
		b = append(b, uintToBytes(i/16)...)

		offset := (i % 16) * 2
		hash := dutyHash(b)
		randomValue := bytesToUint(hash[offset : offset+2])

		effectiveBal := uint64(state.GetEffectiveBalance(candidateIndex))

		if effectiveBal*maxRandomValue >= spec.MaxEffectiveBalanceElectra*randomValue {
			selected = append(selected, candidateIndex)
		}
	}

	return selected, nil
}
