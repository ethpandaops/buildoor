package signer

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"math/big"

	"github.com/tyler-smith/go-bip39"
	"golang.org/x/crypto/hkdf"
)

// blsCurveOrder is the order r of the BLS12-381 scalar field, used to reduce
// derived secret keys per EIP-2333.
var blsCurveOrder, _ = new(big.Int).SetString(
	"73eda753299d7d483339d80809a1d80553bda402fffe5bfeffffffff00000001", 16,
)

// builderKeyPath returns the EIP-2334 validator signing key path nodes for the
// given account index: m / 12381 / 3600 / index / 0 / 0.
func builderKeyPath(index uint64) []uint64 {
	return []uint64{12381, 3600, index, 0, 0}
}

// NewBuilderSigner builds a BLS signer from a builder key source: either a raw hex
// private key or a BIP-39 mnemonic + account index. The mnemonic takes precedence
// when set (callers are expected to enforce mutual exclusivity via config validation).
func NewBuilderSigner(privkeyHex, mnemonic string, index uint64) (*BLSSigner, error) {
	if mnemonic != "" {
		derived, err := DeriveBLSPrivkeyHex(mnemonic, index)
		if err != nil {
			return nil, fmt.Errorf("failed to derive builder key from mnemonic: %w", err)
		}

		privkeyHex = derived
	}

	return NewBLSSigner(privkeyHex)
}

// DeriveBLSPrivkeyHex derives a builder BLS private key from a BIP-39 mnemonic
// and an account index using the standard Ethereum validator key derivation:
// EIP-2333 tree derivation along the EIP-2334 signing key path
// m/12381/3600/{index}/0/0.
//
// It returns the 32-byte secret key as a 64-character lowercase hex string
// (no 0x prefix), matching the format accepted by NewBLSSigner.
func DeriveBLSPrivkeyHex(mnemonic string, index uint64) (string, error) {
	if !bip39.IsMnemonicValid(mnemonic) {
		return "", fmt.Errorf("invalid BIP-39 mnemonic")
	}

	// EIP-2334 encodes each path node as I2OSP(index, 4), so indices must fit in 4 bytes.
	if index > math.MaxUint32 {
		return "", fmt.Errorf("builder key index %d exceeds maximum %d", index, uint64(math.MaxUint32))
	}

	// BIP-39 seed with empty passphrase (the staking-deposit-cli default).
	seed := bip39.NewSeed(mnemonic, "")

	sk := deriveMasterSK(seed)
	for _, node := range builderKeyPath(index) {
		sk = deriveChildSK(sk, node)
	}

	// Serialize the scalar big-endian into exactly 32 bytes (SK < r < 2^255).
	skBytes := make([]byte, 32)
	sk.FillBytes(skBytes)

	return hex.EncodeToString(skBytes), nil
}

// deriveMasterSK implements EIP-2333 derive_master_SK.
func deriveMasterSK(seed []byte) *big.Int {
	return hkdfModR(seed, nil)
}

// deriveChildSK implements EIP-2333 derive_child_SK.
func deriveChildSK(parentSK *big.Int, index uint64) *big.Int {
	return hkdfModR(parentSKToLamportPK(parentSK, index), nil)
}

// hkdfModR implements EIP-2333 HKDF_mod_r, returning a non-zero scalar in [1, r).
func hkdfModR(ikm, keyInfo []byte) *big.Int {
	const l = 48 // octet length of OKM per EIP-2333

	salt := []byte("BLS-SIG-KEYGEN-SALT-")
	sk := new(big.Int)

	// info = key_info || I2OSP(L, 2)
	info := make([]byte, 0, len(keyInfo)+2)
	info = append(info, keyInfo...)
	info = append(info, byte(l>>8), byte(l))

	// ikm' = IKM || I2OSP(0, 1)
	ikmExt := make([]byte, 0, len(ikm)+1)
	ikmExt = append(ikmExt, ikm...)
	ikmExt = append(ikmExt, 0x00)

	for sk.Sign() == 0 {
		h := sha256.Sum256(salt)
		salt = h[:]

		prk := hkdf.Extract(sha256.New, ikmExt, salt)

		okm := make([]byte, l)
		if _, err := io.ReadFull(hkdf.Expand(sha256.New, prk, info), okm); err != nil {
			// SHA256-based HKDF-Expand cannot fail for L=48 (well under 255*32).
			panic(fmt.Sprintf("hkdf expand: %v", err))
		}

		sk.Mod(new(big.Int).SetBytes(okm), blsCurveOrder)
	}

	return sk
}

// parentSKToLamportPK implements EIP-2333 parent_SK_to_lamport_PK.
func parentSKToLamportPK(parentSK *big.Int, index uint64) []byte {
	salt := make([]byte, 4)
	binary.BigEndian.PutUint32(salt, uint32(index))

	ikm := make([]byte, 32)
	parentSK.FillBytes(ikm)

	notIKM := make([]byte, 32)
	for i, b := range ikm {
		notIKM[i] = b ^ 0xFF
	}

	lamport0 := ikmToLamportSK(ikm, salt)
	lamport1 := ikmToLamportSK(notIKM, salt)

	// compressed_lamport_PK = SHA256( SHA256(lamport0[i])... || SHA256(lamport1[i])... )
	buf := make([]byte, 0, lamportChunks*32*2)
	for i := range lamportChunks {
		h := sha256.Sum256(lamport0[i*32 : (i+1)*32])
		buf = append(buf, h[:]...)
	}

	for i := range lamportChunks {
		h := sha256.Sum256(lamport1[i*32 : (i+1)*32])
		buf = append(buf, h[:]...)
	}

	pk := sha256.Sum256(buf)

	return pk[:]
}

// lamportChunks is the number of 32-byte lamport secret chunks per EIP-2333.
const lamportChunks = 255

// ikmToLamportSK implements EIP-2333 IKM_to_lamport_SK, returning
// lamportChunks*32 contiguous bytes (255 32-byte lamport secret chunks).
func ikmToLamportSK(ikm, salt []byte) []byte {
	okm := make([]byte, lamportChunks*32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, ikm, salt, nil), okm); err != nil {
		panic(fmt.Sprintf("hkdf lamport expand: %v", err))
	}

	return okm
}
