// payloadcheck builds a payload on an EL's head through testing_buildBlockV1
// and validates it through engine_newPayload — on the building EL and,
// optionally, on an independent validator EL — so an EL whose testing path
// produces payloads the chain would reject (wrong state root, missing
// requests) is caught before it is relied on for a devnet.
//
//	go run ./.hack/payloadcheck -cl http://cl:4000 -rpc http://el:8545 \
//	  -engine http://el:8551 -jwt /path/jwtsecret -key <hex> \
//	  [-validate-engine http://geth:8551] [-count 3]
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/big"
	"os"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	enginejsonrpc "github.com/ethpandaops/go-eth-engine-client/jsonrpc"
	engineall "github.com/ethpandaops/go-eth-engine-client/spec/all"
	"github.com/ethpandaops/go-eth-engine-client/spec/paris"
	"github.com/ethpandaops/go-eth-engine-client/spec/shanghai"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/rpc/execution"
)

func main() {
	clURL := flag.String("cl", "", "beacon API URL (slot clock, randao, fork)")
	rpcURL := flag.String("rpc", "", "EL JSON-RPC URL with the testing namespace")
	engineURL := flag.String("engine", "", "engine API URL of the building EL")
	validateURL := flag.String("validate-engine", "", "engine API URL of an independent validator EL (optional)")
	jwtPath := flag.String("jwt", "", "JWT secret file")
	keyHex := flag.String("key", "", "funded private key (hex) for the transfers")
	count := flag.Int("count", 3, "transfers to include")
	flag.Parse()

	if *clURL == "" || *rpcURL == "" || *engineURL == "" || *jwtPath == "" || *keyHex == "" {
		flag.Usage()
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	log := logrus.New()
	log.SetLevel(logrus.WarnLevel)

	cl, err := beacon.NewClient(ctx, *clURL, log)
	fatal(err, "beacon client")

	specData, rawData, err := cl.GetRawSpecData(ctx)
	fatal(err, "chain spec")

	spec, err := chain.ParseChainSpec(specData, rawData)
	fatal(err, "parse spec")

	genesis, err := cl.GetGenesis(ctx)
	fatal(err, "genesis")

	el, err := execution.NewClient(ctx, *rpcURL, log)
	fatal(err, "el client")

	head, err := el.GetLatestBlock(ctx)
	fatal(err, "el head")

	chainID, err := el.GetChainID(ctx)
	fatal(err, "chain id")

	// The block after the EL head: next slot's timestamp and fork.
	slotDur := spec.SecondsPerSlot
	slot := uint64(head.Time()-uint64(genesis.GenesisTime.Unix()))/uint64(slotDur.Seconds()) + 1
	timestamp := uint64(genesis.GenesisTime.Unix()) + slot*uint64(slotDur.Seconds())
	epoch := slot / spec.SlotsPerEpoch

	var fork version.DataVersion

	for _, fs := range spec.ForkSchedule {
		if uint64(fs.Epoch) <= epoch && fs.Fork > fork {
			fork = fs.Fork
		}
	}

	engineVersion, err := chain.EngineVersion(fork)
	fatal(err, "engine version")

	key, err := crypto.HexToECDSA(*keyHex)
	fatal(err, "key")

	from := crypto.PubkeyToAddress(key.PublicKey)

	nonce, err := el.GetConfirmedNonce(ctx, from)
	fatal(err, "nonce")

	signer := types.LatestSignerForChainID(chainID)
	baseFee := head.BaseFee()
	feeCap := new(big.Int).Mul(baseFee, big.NewInt(4))
	feeCap.Add(feeCap, big.NewInt(2_000_000_000))

	txs := make([][]byte, 0, *count)
	hashes := make([]string, 0, *count)

	for i := 0; i < *count; i++ {
		tx := types.MustSignNewTx(key, signer, &types.DynamicFeeTx{
			ChainID:   chainID,
			Nonce:     nonce + uint64(i),
			GasTipCap: big.NewInt(1_000_000_000),
			GasFeeCap: feeCap,
			Gas:       21000,
			To:        &common.Address{0xde, 0xad},
			Value:     big.NewInt(1),
		})

		raw, err := tx.MarshalBinary()
		fatal(err, "encode tx")

		txs = append(txs, raw)
		hashes = append(hashes, tx.Hash().Hex())
	}

	// Attributes: randao and beacon root do not affect execution validity
	// (the EL stores them), so placeholders are fine for the check.
	attrs := &engineall.PayloadAttributes{
		Version:               engineVersion,
		Timestamp:             timestamp,
		PrevRandao:            paris.Hash32{1},
		SuggestedFeeRecipient: paris.Address(from),
		Withdrawals:           []*shanghai.Withdrawal{}, // the list is mandatory (erigon refuses null)
		ParentBeaconBlockRoot: paris.Hash32{2},
		SlotNumber:            slot,
		TargetGasLimit:        head.GasLimit(),
	}

	engine, err := enginejsonrpc.New(ctx,
		enginejsonrpc.WithAddress(*engineURL),
		enginejsonrpc.WithJWTSecretFile(*jwtPath),
		enginejsonrpc.WithLogger(log))
	fatal(err, "engine client")

	// Pin the EL head (geth/ethrex build on their head only).
	fcu, err := engine.ForkchoiceUpdatedAgnostic(ctx, &engineall.ForkchoiceUpdatedRequest{
		Version: engineVersion,
		ForkchoiceState: &paris.ForkchoiceState{
			HeadBlockHash: paris.Hash32(head.Hash()), SafeBlockHash: paris.Hash32(head.Hash()), FinalizedBlockHash: paris.Hash32(head.Hash()),
		},
	})
	fatal(err, "forkchoiceUpdated")
	fmt.Printf("parent %s (number %d, fork %s/%s) fcu=%s\n", head.Hash().Hex()[:18], head.NumberU64(), fork, engineVersion, fcu.PayloadStatus.Status)

	started := time.Now()
	resp, err := el.BuildBlockV1(ctx, engineVersion, head.Hash(), attrs, txs, nil)
	fatal(err, "testing_buildBlockV1")

	p := resp.ExecutionPayload
	fmt.Printf("built  %s in %s: %d txs, gasUsed %d, value %s\n", common.Hash(p.BlockHash).Hex()[:18], time.Since(started).Round(time.Millisecond), len(p.Transactions), p.GasUsed, resp.BlockValue)

	if len(p.Transactions) != len(txs) {
		fmt.Printf("MISMATCH: submitted %d, built %d\n", len(txs), len(p.Transactions))
	} else {
		for i, raw := range p.Transactions {
			tx := new(types.Transaction)
			if err := tx.UnmarshalBinary(raw); err != nil || tx.Hash().Hex() != hashes[i] {
				fmt.Printf("MISMATCH at %d\n", i)
			}
		}
	}

	req := &engineall.NewPayloadRequest{
		Version:               engineVersion,
		ExecutionPayload:      p,
		ParentBeaconBlockRoot: attrs.ParentBeaconBlockRoot,
		ExecutionRequests:     resp.ExecutionRequests,
	}

	report := func(label string, e *enginejsonrpc.Service) {
		status, err := e.NewPayloadAgnostic(ctx, req)
		if err != nil {
			fmt.Printf("%-10s newPayload error: %v\n", label, err)

			return
		}

		fmt.Printf("%-10s newPayload %s %s\n", label, status.Status, string(status.ValidationError))
	}

	report("builder", engine)

	if *validateURL != "" {
		v, err := enginejsonrpc.New(ctx,
			enginejsonrpc.WithAddress(*validateURL),
			enginejsonrpc.WithJWTSecretFile(*jwtPath),
			enginejsonrpc.WithLogger(log))
		fatal(err, "validator engine client")
		report("validator", v)
	}
}

func fatal(err error, what string) {
	if err != nil {
		log.Fatalf("%s: %v", what, err)
	}
}
