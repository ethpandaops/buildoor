package config

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedactRPCUpstreams(t *testing.T) {
	cases := map[string]string{
		"local":                     "local",
		"local@0, http://el:8545@1": "local@0,http://el:8545@1",
		"https://user:secret@rpc.example.com/v3/key@2": "https://***@rpc.example.com/***@2",
		"https://rpc.example.com/?apikey=secret":       "https://rpc.example.com/***",
		"wss://rpc.example.com/ws/secret":              "wss://rpc.example.com/***",
		"http://el:8545/":                              "http://el:8545/",
		"https://rpc.example.com/v2/secret, local@5":   "https://rpc.example.com/***,local@5",
	}

	for spec, want := range cases {
		require.Equal(t, want, RedactRPCUpstreams(spec), spec)
	}
}

func TestParseRPCUpstreamsRefusesRedactedEntries(t *testing.T) {
	_, err := ParseRPCUpstreams(RedactRPCUpstreams("https://user:secret@rpc.example.com/key"))
	require.ErrorContains(t, err, "redacted")

	upstreams, err := ParseRPCUpstreams("https://user:secret@rpc.example.com/key@1")
	require.NoError(t, err)
	require.Equal(t, []RPCUpstream{{Target: "https://user:secret@rpc.example.com/key", Priority: 1}}, upstreams)
}

// The config's JSON form is public (REST + SSE): no key, secret or client
// endpoint may appear in it.
func TestConfigJSONLeavesOutPrivateFields(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BuilderPrivkey = "builder-secret"
	cfg.BuilderMnemonic = "mnemonic-secret"
	cfg.WalletPrivkey = "wallet-secret"
	cfg.ELJWTSecret = "/jwt-secret"
	cfg.CLClient = "http://user:cl-secret@cl"
	cfg.ELEngineAPI = "http://engine-secret"
	cfg.ELRPC = "http://rpc-secret"
	cfg.ELWS = "ws://ws-secret"
	cfg.AuthProviderURL = "https://auth-secret"
	cfg.InjectHeadHTML = "<script>head-secret</script>"
	cfg.StateDBPath = "/state-secret.db"
	cfg.ValidatorRanges = ValidatorRangesConfig{File: "/ranges-secret", URL: "https://ranges-secret"}
	cfg.TxPool.AuthToken = "token-secret"
	cfg.TxPool.StoreDir = "/store-secret"

	data, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NotContains(t, string(data), "secret")
}
