// The gblsminsig package requires cgo,
// therefore this file also requires cgo.

//go:build cgo

package tmsqlite_test

import (
	"testing"

	"github.com/gordian-engine/gordian/gcrypto/gblsminsig"
	"github.com/gordian-engine/gordian/gcrypto/gblsminsig/gblsminsigtest"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"
)

func init() {
	// Share the same registry as used in store_test.go.
	gblsminsig.Register(&reg)
}

func blsFixtureFactory(nVals int) *tmconsensustest.Fixture {
	privVals := make(tmconsensustest.PrivVals, nVals)
	signers := gblsminsigtest.DeterministicSigners(nVals)

	for i := range privVals {
		privVals[i] = tmconsensustest.PrivVal{
			Val: tmconsensus.Validator{
				PubKey: signers[i].PubKey().(gblsminsig.PubKey),

				// Order by power descending,
				// with the power difference being negligible,
				// so that the validator order matches the default deterministic key order.
				// (Without this power adjustment, the validators would be ordered
				// by public key or by ID, which is unlikely to match their order
				// as defined in fixtures or other uses of determinsitic validators.
				Power: uint64(100_000 - i),
			},
			Signer: signers[i],
		}
	}

	fx := &tmconsensustest.Fixture{
		PrivVals: privVals,

		SignatureScheme:                   tmconsensustest.SimpleSignatureScheme{},
		HashScheme:                        tmconsensustest.SimpleHashScheme{},
		CommonMessageSignatureProofScheme: gblsminsig.SignatureProofScheme{},

		Registry: reg,

		// The fixture also has prevCommitProof and prevAppStateHash fields,
		// which are unexported so we can't access them from this package.
		// Tests are passing currently, but the inability to set those fields
		// seems likely to cause an issue at some point.
	}

	// Temporary hack to ensure that the fixture's unexported fields are set.
	// Without this, tests panic on "NOT NULL constraint failed: headers.prev_app_state_hash".
	// But instead of requiring this, we should change Gordian core
	// to avoid this extra work for the test importer.
	if nVals > 0 {
		_ = fx.DefaultGenesis()
	}
	return fx
}

func TestBLSStoreCompliance(t *testing.T) {
	testStoreCompliance(t, blsFixtureFactory)
}
