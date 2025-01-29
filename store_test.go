package tmsqlite_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/gordian-engine/gordian/tm/tmstore"
	"github.com/gordian-engine/gordian/tm/tmstore/tmstoretest"
	"github.com/gordian-engine/tmsqlite"
	"github.com/stretchr/testify/require"
)

var reg gcrypto.Registry

func init() {
	gcrypto.RegisterEd25519(&reg)
}

// testStoreCompliance is the unexported implementation of all the store compliance tests.
// This enables simple testing of
func testStoreCompliance(t *testing.T, fxf tmstoretest.FixtureFactory) {
	hs := fxf(0).HashScheme
	t.Run("New", func(t *testing.T) {
		t.Parallel()

		// Just create the database and close it successfully.
		s, err := tmsqlite.NewInMemStore(context.Background(), hs, &reg)
		require.NoError(t, err)
		require.NotNil(t, s)

		// Helpful output in the simplest test, if there is uncertainty which type was built.
		t.Logf("Tests are for build type %s", s.BuildType)

		require.NoError(t, s.Close())
	})

	t.Run("Migrate", func(t *testing.T) {
		t.Run("empty database", func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "db.sqlite")
			s1, err := tmsqlite.NewOnDiskStore(context.Background(), path, hs, &reg)
			require.NoError(t, err)
			require.NotNil(t, s1)
			require.NoError(t, s1.Close())

			s2, err := tmsqlite.NewOnDiskStore(context.Background(), path, hs, &reg)
			require.NoError(t, err)
			require.NotNil(t, s2)
			require.NoError(t, s2.Close())
		})
	})

	t.Run("ActionStore", func(t *testing.T) {
		t.Parallel()

		tmstoretest.TestActionStoreCompliance(t, func(cleanup func(func())) (tmstore.ActionStore, error) {
			s, err := tmsqlite.NewInMemStore(context.Background(), hs, &reg)
			if err != nil {
				return nil, err
			}
			cleanup(func() {
				require.NoError(t, s.Close())
			})
			return s, nil
		}, fxf)
	})

	t.Run("FinalizationStore", func(t *testing.T) {
		t.Parallel()

		tmstoretest.TestFinalizationStoreCompliance(t, func(cleanup func(func())) (tmstore.FinalizationStore, error) {
			s, err := tmsqlite.NewInMemStore(context.Background(), hs, &reg)
			if err != nil {
				return nil, err
			}
			cleanup(func() {
				require.NoError(t, s.Close())
			})
			return s, nil
		}, fxf)
	})

	t.Run("CommittedHeaderStore", func(t *testing.T) {
		t.Parallel()

		tmstoretest.TestCommittedHeaderStoreCompliance(t, func(cleanup func(func())) (tmstore.CommittedHeaderStore, error) {
			s, err := tmsqlite.NewInMemStore(context.Background(), hs, &reg)
			if err != nil {
				return nil, err
			}
			cleanup(func() {
				require.NoError(t, s.Close())
			})
			return s, nil
		}, fxf)
	})

	t.Run("MirrorStore", func(t *testing.T) {
		t.Parallel()

		tmstoretest.TestMirrorStoreCompliance(t, func(cleanup func(func())) (tmstore.MirrorStore, error) {
			s, err := tmsqlite.NewInMemStore(context.Background(), hs, &reg)
			if err != nil {
				return nil, err
			}
			cleanup(func() {
				require.NoError(t, s.Close())
			})
			return s, nil
		}) // MirrorStore does not require a consensus fixture.
	})

	t.Run("RoundStore", func(t *testing.T) {
		t.Parallel()

		tmstoretest.TestRoundStoreCompliance(t, func(cleanup func(func())) (tmstore.RoundStore, error) {
			s, err := tmsqlite.NewInMemStore(context.Background(), hs, &reg)
			if err != nil {
				return nil, err
			}
			cleanup(func() {
				require.NoError(t, s.Close())
			})
			return s, nil
		}, fxf)
	})

	t.Run("StateMachine", func(t *testing.T) {
		t.Parallel()

		tmstoretest.TestStateMachineStoreCompliance(t, func(ctx context.Context, cleanup func(func())) (tmstore.StateMachineStore, error) {
			s, err := tmsqlite.NewInMemStore(ctx, hs, &reg)
			if err != nil {
				return nil, err
			}
			cleanup(func() {
				require.NoError(t, s.Close())
			})
			return s, nil
		}) // StateMachineStore does not require a consensus fixture.
	})

	t.Run("ValidatorStore", func(t *testing.T) {
		t.Parallel()

		tmstoretest.TestValidatorStoreCompliance(t, func(cleanup func(func())) (tmstore.ValidatorStore, error) {
			s, err := tmsqlite.NewInMemStore(context.Background(), hs, &reg)
			if err != nil {
				return nil, err
			}
			cleanup(func() {
				require.NoError(t, s.Close())
			})
			return s, nil
		}, fxf)
	})

	t.Run("MultiStore", func(t *testing.T) {
		t.Parallel()

		tmstoretest.TestMultiStoreCompliance(t, func(cleanup func(func())) (*tmsqlite.Store, error) {
			s, err := tmsqlite.NewInMemStore(context.Background(), hs, &reg)
			if err != nil {
				return nil, err
			}
			cleanup(func() {
				require.NoError(t, s.Close())
			})
			return s, nil
		}, fxf)
	})
}

func TestStoreCompliance_ed25519(t *testing.T) {
	testStoreCompliance(t, tmconsensustest.NewEd25519Fixture)
}
