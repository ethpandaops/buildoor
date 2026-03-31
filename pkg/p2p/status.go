package p2p

import (
	"encoding/binary"
	"fmt"

	"github.com/golang/snappy"
	"github.com/multiformats/go-varint"
)

// StatusMessage is the SSZ-encoded StatusV2 message sent/received over libp2p.
// Per the Ethereum consensus p2p spec (Fulu/Gloas):
//
//	ForkDigest            [4]byte   — identifies the chain/fork
//	FinalizedRoot         [32]byte  — our finalized checkpoint root
//	FinalizedEpoch        uint64    — our finalized checkpoint epoch (little-endian)
//	HeadRoot              [32]byte  — current head block root
//	HeadSlot              uint64    — current head slot (little-endian)
//	EarliestAvailableSlot uint64    — earliest slot we can serve data for (little-endian)
//
// Total: 4 + 32 + 8 + 32 + 8 + 8 = 92 bytes (fixed-size SSZ).
const statusV2SSZSize = 92

// StatusMessage represents the StatusV2 p2p message used from Fulu/Gloas onwards.
type StatusMessage struct {
	ForkDigest            [4]byte
	FinalizedRoot         [32]byte
	FinalizedEpoch        uint64
	HeadRoot              [32]byte
	HeadSlot              uint64
	EarliestAvailableSlot uint64
}

// MarshalSSZ serializes the StatusMessage into its fixed-size SSZ representation.
func (s *StatusMessage) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, statusV2SSZSize)
	copy(buf[0:4], s.ForkDigest[:])
	copy(buf[4:36], s.FinalizedRoot[:])
	binary.LittleEndian.PutUint64(buf[36:44], s.FinalizedEpoch)
	copy(buf[44:76], s.HeadRoot[:])
	binary.LittleEndian.PutUint64(buf[76:84], s.HeadSlot)
	binary.LittleEndian.PutUint64(buf[84:92], s.EarliestAvailableSlot)
	return buf, nil
}

// UnmarshalSSZ deserializes a StatusMessage from raw SSZ bytes.
func (s *StatusMessage) UnmarshalSSZ(buf []byte) error {
	if len(buf) != statusV2SSZSize {
		return fmt.Errorf("status SSZ: expected %d bytes, got %d", statusV2SSZSize, len(buf))
	}

	copy(s.ForkDigest[:], buf[0:4])
	copy(s.FinalizedRoot[:], buf[4:36])
	s.FinalizedEpoch = binary.LittleEndian.Uint64(buf[36:44])
	copy(s.HeadRoot[:], buf[44:76])
	s.HeadSlot = binary.LittleEndian.Uint64(buf[76:84])
	s.EarliestAvailableSlot = binary.LittleEndian.Uint64(buf[84:92])

	return nil
}

// EncodeStatusMessage serializes a StatusMessage in Prysm's ssz_snappy RPC wire format:
// <uvarint length prefix of uncompressed SSZ size><snappy-compressed SSZ bytes>
func EncodeStatusMessage(msg *StatusMessage) ([]byte, error) {
	ssz, err := msg.MarshalSSZ()
	if err != nil {
		return nil, fmt.Errorf("marshal SSZ: %w", err)
	}

	compressed := snappy.Encode(nil, ssz)

	lenPrefix := varint.ToUvarint(uint64(len(ssz)))

	out := make([]byte, len(lenPrefix)+len(compressed))
	copy(out, lenPrefix)
	copy(out[len(lenPrefix):], compressed)

	return out, nil
}

// DecodeStatusMessage reads a StatusMessage from Prysm's ssz_snappy RPC wire format:
// <uvarint length prefix><snappy-compressed SSZ bytes>
// Returns the message and the number of bytes consumed.
func DecodeStatusMessage(data []byte) (*StatusMessage, int, error) {
	ssZLen, n, err := varint.FromUvarint(data)
	if err != nil {
		return nil, 0, fmt.Errorf("read varint: %w", err)
	}

	if ssZLen != statusV2SSZSize {
		return nil, 0, fmt.Errorf("unexpected SSZ length %d (expected %d)", ssZLen, statusV2SSZSize)
	}

	compressed := data[n:]

	decompressed, err := snappy.Decode(nil, compressed)
	if err != nil {
		return nil, 0, fmt.Errorf("snappy decode: %w", err)
	}

	msg := &StatusMessage{}
	if err := msg.UnmarshalSSZ(decompressed); err != nil {
		return nil, 0, fmt.Errorf("unmarshal SSZ: %w", err)
	}

	return msg, n + len(compressed), nil
}
