// Package signer provides BLS signing utilities for builder operations.
package signer

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/herumi/bls-eth-go-binary/bls"
)

var initOnce sync.Once

// Domain types for lifecycle operations.
var (
	// DomainVoluntaryExit is the standard domain for voluntary exits.
	DomainVoluntaryExit = phase0.DomainType{0x04, 0x00, 0x00, 0x00}

	// DomainDeposit is the standard domain for deposit signatures.
	DomainDeposit = phase0.DomainType{0x03, 0x00, 0x00, 0x00}
)

// initBLS initializes the BLS library with BLS12-381 curve.
func initBLS() {
	initOnce.Do(func() {
		if err := bls.Init(bls.BLS12_381); err != nil {
			panic(fmt.Sprintf("failed to initialize BLS library: %v", err))
		}

		if err := bls.SetETHmode(bls.EthModeLatest); err != nil {
			panic(fmt.Sprintf("failed to set ETH mode: %v", err))
		}
	})
}

// BLSSigner handles BLS signing operations for a builder.
type BLSSigner struct {
	secretKey   *bls.SecretKey
	publicKey   *bls.PublicKey
	pubkeyBytes phase0.BLSPubKey
}

// NewBLSSigner creates a new BLS signer from a hex-encoded private key.
func NewBLSSigner(privkeyHex string) (*BLSSigner, error) {
	initBLS()

	privkeyHex = strings.TrimPrefix(privkeyHex, "0x")

	privkeyBytes, err := hex.DecodeString(privkeyHex)
	if err != nil {
		return nil, fmt.Errorf("failed to decode private key hex: %w", err)
	}

	if len(privkeyBytes) != 32 {
		return nil, fmt.Errorf("private key must be 32 bytes, got %d", len(privkeyBytes))
	}

	secretKey := new(bls.SecretKey)
	if err := secretKey.Deserialize(privkeyBytes); err != nil {
		return nil, fmt.Errorf("failed to deserialize secret key: %w", err)
	}

	publicKey := secretKey.GetPublicKey()

	var pubkeyBytes phase0.BLSPubKey

	copy(pubkeyBytes[:], publicKey.Serialize())

	return &BLSSigner{
		secretKey:   secretKey,
		publicKey:   publicKey,
		pubkeyBytes: pubkeyBytes,
	}, nil
}

// PublicKey returns the BLS public key.
func (s *BLSSigner) PublicKey() phase0.BLSPubKey {
	return s.pubkeyBytes
}

// PublicKeyBytes returns the public key as a byte slice.
func (s *BLSSigner) PublicKeyBytes() []byte {
	return s.pubkeyBytes[:]
}

// Sign signs a message and returns the signature.
func (s *BLSSigner) Sign(message []byte) (phase0.BLSSignature, error) {
	sig := s.secretKey.SignByte(message)

	var sigBytes phase0.BLSSignature
	copy(sigBytes[:], sig.Serialize())

	return sigBytes, nil
}

// SignWithDomain signs a root with a domain and returns the signature.
func (s *BLSSigner) SignWithDomain(root phase0.Root, domain phase0.Domain) (phase0.BLSSignature, error) {
	signingRoot := ComputeSigningRoot(root, domain)

	return s.Sign(signingRoot[:])
}

// ComputeDomain computes a domain value for a given domain type and fork version.
func ComputeDomain(
	domainType phase0.DomainType,
	forkVersion phase0.Version,
	genesisValidatorsRoot phase0.Root,
) phase0.Domain {
	// Compute fork data root
	forkDataRoot := computeForkDataRoot(forkVersion, genesisValidatorsRoot)

	// Domain = domain_type + fork_data_root[:28]
	var domain phase0.Domain

	copy(domain[:4], domainType[:])
	copy(domain[4:], forkDataRoot[:28])

	return domain
}

// computeForkDataRoot computes the fork data root from fork version and genesis validators root.
func computeForkDataRoot(forkVersion phase0.Version, genesisValidatorsRoot phase0.Root) phase0.Root {
	// ForkData{current_version: forkVersion, genesis_validators_root: genesisValidatorsRoot}
	// Hash tree root of ForkData
	var forkData [64]byte

	copy(forkData[:4], forkVersion[:])
	copy(forkData[32:], genesisValidatorsRoot[:])

	hash := sha256.Sum256(forkData[:])

	var root phase0.Root
	copy(root[:], hash[:])

	return root
}

// ComputeSigningRoot computes the signing root from an object root and domain.
func ComputeSigningRoot(objectRoot phase0.Root, domain phase0.Domain) phase0.Root {
	// SigningData{object_root: objectRoot, domain: domain}
	var signingData [64]byte

	copy(signingData[:32], objectRoot[:])
	copy(signingData[32:], domain[:])

	hash := sha256.Sum256(signingData[:])

	var root phase0.Root
	copy(root[:], hash[:])

	return root
}

// ComputeDepositSigningRoot computes the signing root for a deposit message.
// Uses the go-eth2-client library's SSZ implementation for correctness.
// Per spec, uses GENESIS_FORK_VERSION and zeros for genesis_validators_root.
func ComputeDepositSigningRoot(
	pubkey phase0.BLSPubKey,
	withdrawalCredentials [32]byte,
	amountGwei uint64,
	genesisForkVersion phase0.Version,
) (phase0.Root, error) {
	// Create DepositMessage using the library type
	depositMsg := &phase0.DepositMessage{
		PublicKey:             pubkey,
		WithdrawalCredentials: withdrawalCredentials[:],
		Amount:                phase0.Gwei(amountGwei),
	}

	// Compute hash tree root using the library's SSZ implementation
	depositMsgRoot, err := depositMsg.HashTreeRoot()
	if err != nil {
		return phase0.Root{}, fmt.Errorf("failed to compute deposit message root: %w", err)
	}

	var root phase0.Root
	copy(root[:], depositMsgRoot[:])

	// Compute domain with GENESIS_FORK_VERSION and empty genesis_validators_root (per spec)
	// For deposits, genesis_validators_root is always zero
	var emptyRoot phase0.Root

	domain := ComputeDomain(DomainDeposit, genesisForkVersion, emptyRoot)

	return ComputeSigningRoot(root, domain), nil
}

// ComputeDepositDataRoot computes the hash tree root of DepositData.
// Uses the go-eth2-client library's SSZ implementation for correctness.
func ComputeDepositDataRoot(
	pubkey phase0.BLSPubKey,
	withdrawalCredentials [32]byte,
	amountGwei uint64,
	signature phase0.BLSSignature,
) (phase0.Root, error) {
	// Create DepositData using the library type
	depositData := &phase0.DepositData{
		PublicKey:             pubkey,
		WithdrawalCredentials: withdrawalCredentials[:],
		Amount:                phase0.Gwei(amountGwei),
		Signature:             signature,
	}

	// Compute hash tree root using the library's SSZ implementation
	dataRoot, err := depositData.HashTreeRoot()
	if err != nil {
		return phase0.Root{}, fmt.Errorf("failed to compute deposit data root: %w", err)
	}

	var root phase0.Root
	copy(root[:], dataRoot[:])

	return root, nil
}

// ComputeVoluntaryExitRoot computes the hash tree root of a VoluntaryExit.
func ComputeVoluntaryExitRoot(epoch phase0.Epoch, validatorIndex phase0.ValidatorIndex) phase0.Root {
	// VoluntaryExit has 2 fields:
	// - epoch: uint64
	// - validator_index: uint64
	var data [64]byte

	binary.LittleEndian.PutUint64(data[:8], uint64(epoch))
	binary.LittleEndian.PutUint64(data[32:40], uint64(validatorIndex))

	hash := sha256.Sum256(data[:])

	var root phase0.Root
	copy(root[:], hash[:])

	return root
}

// SignVoluntaryExit signs a voluntary exit message.
func (s *BLSSigner) SignVoluntaryExit(
	epoch phase0.Epoch,
	validatorIndex phase0.ValidatorIndex,
	forkVersion phase0.Version,
	genesisValidatorsRoot phase0.Root,
) (phase0.BLSSignature, error) {
	exitRoot := ComputeVoluntaryExitRoot(epoch, validatorIndex)
	domain := ComputeDomain(DomainVoluntaryExit, forkVersion, genesisValidatorsRoot)

	return s.SignWithDomain(exitRoot, domain)
}
