// txgen signs simple transfers from one or more private keys and submits them
// to a JSON-RPC endpoint (typically buildoor's tx intake at /rpc). It prints
// the transaction hashes in submission order, one per line, so a caller can
// turn them into an explicit per-slot plan (action plan build.txs).
//
//	go run ./.hack/txgen -rpc http://127.0.0.1:8080/rpc -keys k1,k2 -count 3
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

func main() {
	rpcURL := flag.String("rpc", "http://127.0.0.1:8080/rpc", "JSON-RPC endpoint (buildoor intake)")
	keys := flag.String("keys", "", "comma-separated hex private keys")
	count := flag.Int("count", 1, "transactions per key")
	feeGwei := flag.Int64("fee", 100, "max fee per gas in gwei")
	flag.Parse()

	if *keys == "" {
		log.Fatal("-keys is required")
	}

	ctx := context.Background()

	client, err := ethclient.DialContext(ctx, *rpcURL)
	if err != nil {
		log.Fatalf("dial: %v", err)
	}

	chainID, err := client.ChainID(ctx)
	if err != nil {
		log.Fatalf("chain id: %v", err)
	}

	signer := types.LatestSignerForChainID(chainID)

	for _, keyHex := range strings.Split(*keys, ",") {
		key, err := crypto.HexToECDSA(strings.TrimPrefix(strings.TrimSpace(keyHex), "0x"))
		if err != nil {
			log.Fatalf("key: %v", err)
		}

		from := crypto.PubkeyToAddress(key.PublicKey)

		// The intake answers "pending" from its queue on top of the EL nonce.
		nonce, err := client.PendingNonceAt(ctx, from)
		if err != nil {
			log.Fatalf("nonce: %v", err)
		}

		for i := 0; i < *count; i++ {
			tx := types.MustSignNewTx(key, signer, &types.DynamicFeeTx{
				ChainID:   chainID,
				Nonce:     nonce + uint64(i),
				GasTipCap: big.NewInt(1_000_000_000),
				GasFeeCap: big.NewInt(*feeGwei * 1_000_000_000),
				Gas:       21000,
				To:        &common.Address{0xde, 0xad},
				Value:     big.NewInt(1),
			})

			if err := client.SendTransaction(ctx, tx); err != nil {
				log.Fatalf("send %s nonce %d: %v", from.Hex(), nonce+uint64(i), err)
			}

			fmt.Println(tx.Hash().Hex())
		}
	}
}
