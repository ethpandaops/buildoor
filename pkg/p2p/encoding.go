// Package p2p provides a lightweight libp2p host for gossip topic subscriptions.
package p2p

import (
	"fmt"

	"github.com/golang/snappy"
)

// SSZUnmarshaler is the interface for types that can be unmarshaled from SSZ.
type SSZUnmarshaler interface {
	UnmarshalSSZ(buf []byte) error
}

// DecodeGossipMessage decodes a snappy-compressed SSZ-encoded gossip message.
func DecodeGossipMessage(data []byte, dest SSZUnmarshaler) error {
	decoded, err := snappy.Decode(nil, data)
	if err != nil {
		return fmt.Errorf("snappy decode failed: %w", err)
	}

	if err := dest.UnmarshalSSZ(decoded); err != nil {
		return fmt.Errorf("SSZ unmarshal failed: %w", err)
	}

	return nil
}
