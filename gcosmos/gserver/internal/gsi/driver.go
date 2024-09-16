package gsi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
	"runtime/trace"
	"slices"
	"time"

	corecomet "cosmossdk.io/core/comet"
	corecontext "cosmossdk.io/core/context"
	coreserver "cosmossdk.io/core/server"
	"cosmossdk.io/core/store"
	"cosmossdk.io/core/transaction"
	"cosmossdk.io/server/v2/appmanager"
	"cosmossdk.io/store/v2/root"
	consensustypes "cosmossdk.io/x/consensus/types"
	cometapitypes "github.com/cometbft/cometbft/api/cometbft/types/v1"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/server"
	genutiltypes "github.com/cosmos/cosmos-sdk/x/genutil/types"
	"github.com/rollchains/gordian/gcosmos/gccodec"
	"github.com/rollchains/gordian/gcosmos/gserver/internal/gp2papi"
	"github.com/rollchains/gordian/gcosmos/gserver/internal/gsbd"
	"github.com/rollchains/gordian/gcrypto"
	"github.com/rollchains/gordian/gdriver/gdatapool"
	"github.com/rollchains/gordian/internal/gchan"
	"github.com/rollchains/gordian/internal/glog"
	"github.com/rollchains/gordian/tm/tmconsensus"
	"github.com/rollchains/gordian/tm/tmdriver"
	"github.com/rollchains/gordian/tm/tmengine/tmelink"
)

type DriverConfig struct {
	ChainID string

	ConsensusAuthority string

	AppManager *appmanager.AppManager[transaction.Tx]

	Store *root.Store

	InitChainRequests     <-chan tmdriver.InitChainRequest
	FinalizeBlockRequests <-chan tmdriver.FinalizeBlockRequest

	LagStateUpdates <-chan tmelink.LagState

	TxBuffer *SDKTxBuf

	DataPool *gdatapool.Pool[[]transaction.Tx]

	SyncClient *gp2papi.SyncClient
}

type Driver struct {
	log *slog.Logger

	chainID string

	txBuf *SDKTxBuf

	pool *gdatapool.Pool[[]transaction.Tx]

	sClient *gp2papi.SyncClient

	am       *appmanager.AppManager[transaction.Tx]
	sdkStore *root.Store

	finalizeBlockRequests <-chan tmdriver.FinalizeBlockRequest

	lagStateUpdates <-chan tmelink.LagState

	done chan struct{}
}

func NewDriver(
	lifeCtx, valCtx context.Context,
	log *slog.Logger,
	cfg DriverConfig,
) (*Driver, error) {
	cc := valCtx.Value(client.ClientContextKey).(*client.Context)
	serverCtx := valCtx.Value(server.ServerContextKey).(*server.Context)

	genesisPath := filepath.Join(cc.HomeDir, serverCtx.Config.Genesis)

	ag, err := genutiltypes.AppGenesisFromFile(genesisPath)
	if err != nil {
		return nil, fmt.Errorf("failed to run AppGenesisFromFile(%s): %w", genesisPath, err)
	}

	d := &Driver{
		log: log,

		chainID: cfg.ChainID,

		txBuf: cfg.TxBuffer,

		pool: cfg.DataPool,

		sClient: cfg.SyncClient,

		finalizeBlockRequests: cfg.FinalizeBlockRequests,
		lagStateUpdates:       cfg.LagStateUpdates,

		am:       cfg.AppManager,
		sdkStore: cfg.Store,

		done: make(chan struct{}),
	}

	go d.run(lifeCtx, ag, cc.TxConfig, cfg)

	return d, nil
}

func (d *Driver) run(
	ctx context.Context,
	ag *genutiltypes.AppGenesis,
	txConfig client.TxConfig,
	cfg DriverConfig,
) {
	ctx, task := trace.NewTask(ctx, "gsi.Driver.run")
	defer task.End()

	defer close(d.done)

	d.log.Info("Driver starting...")
	defer d.log.Info("Driver goroutine finished")

	// We are currently assuming we always need to handle init chain,
	// but we should handle non-initial height.
	if !d.handleInitialization(
		ctx,
		ag,
		cfg.ConsensusAuthority,
		cfg.Store,
		txConfig,
		cfg.InitChainRequests,
	) {
		return
	}

	d.mainLoop(ctx)
}

func (d *Driver) handleInitialization(
	ctx context.Context,
	ag *genutiltypes.AppGenesis,
	consensusAuthority string,
	s *root.Store,
	txConfig client.TxConfig,
	initChainCh <-chan tmdriver.InitChainRequest,
) bool {
	defer trace.StartRegion(ctx, "handleInitialization").End()

	req, ok := gchan.RecvC(ctx, d.log, initChainCh, "receiving init chain request")
	if !ok {
		d.log.Warn("Context cancelled before receiving init chain message")
		return false
	}
	d.log.Info("Got init chain request", "val", req)

	blockReq := &coreserver.BlockRequest[transaction.Tx]{
		Height: req.Genesis.InitialHeight - 1,

		ChainId:   req.Genesis.ChainID,
		IsGenesis: true,

		// TODO: these should be actually calculated.
		// Right now they are just appropriate sized to get past initial validation inside the SDK.
		Hash:    make([]byte, 32),
		AppHash: make([]byte, 32),

		// Omitted vs comet server code: Time, Hash, AppHash
	}

	appState := []byte(ag.AppState)

	// Now, init genesis in the SDK-layer application.
	var codec transaction.Codec[transaction.Tx] = gccodec.NewTxDecoder(txConfig)

	// We need a special context for the InitGenesis call,
	// as the consensus parameters are expected to be a value on the context there.
	deliverCtx := context.WithValue(ctx, corecontext.CometParamsInitInfoKey, &consensustypes.MsgUpdateParams{
		Authority: consensusAuthority,
		Block: &cometapitypes.BlockParams{
			// Just setting these to something non-zero for now.
			MaxBytes: -1,
			MaxGas:   -1,
		},
		Evidence: &cometapitypes.EvidenceParams{
			// Completely arbitrary non-zero values.
			MaxAgeNumBlocks: 10,
			MaxAgeDuration:  10 * time.Minute,
			MaxBytes:        1024,
		},
		Validator: &cometapitypes.ValidatorParams{
			PubKeyTypes: []string{"ed25519"},
		},
	})
	blockResp, genesisState, err := d.am.InitGenesis(
		deliverCtx, blockReq, appState, codec,
	)
	if err != nil {
		d.log.Warn("Failed to run appManager.InitGenesis", "appState", fmt.Sprintf("%q", appState), "err", err)
		return false
	}

	// Set the initial state on the transaction buffer.
	d.txBuf.Initialize(ctx, genesisState)

	// SetInitialVersion followed by WorkingHash,
	// and passing that hash as the initial app state hash,
	// is what the comet server code does.
	if err := s.SetInitialVersion(req.Genesis.InitialHeight); err != nil {
		d.log.Warn("Failed to set initial version", "err", err)
		return false
	}

	stateChanges, err := genesisState.GetStateChanges()
	if err != nil {
		d.log.Warn("Failed to set get state changes from genesis state", "err", err)
		return false
	}
	stateRoot, err := s.WorkingHash(&store.Changeset{
		Changes: stateChanges,
	})
	if err != nil {
		d.log.Warn("Failed to get working hash from changeset", "err", err)
		return false
	}

	gVals := make([]tmconsensus.Validator, len(blockResp.ValidatorUpdates))
	for i, vu := range blockResp.ValidatorUpdates {
		if vu.PubKeyType != "ed25519" {
			panic(fmt.Errorf(
				"TODO: handle validator with non-ed25519 key type: %q",
				vu.PubKeyType,
			))
		}
		pk, err := gcrypto.NewEd25519PubKey(vu.PubKey)
		if err != nil {
			panic(fmt.Errorf(
				"BUG: NewEd25519PubKey should never error; got %w", err,
			))
		}
		gVals[i] = tmconsensus.Validator{
			PubKey: pk,
			Power:  uint64(vu.Power),
		}
	}

	resp := tmdriver.InitChainResponse{
		AppStateHash: stateRoot,

		Validators: gVals,
	}
	if !gchan.SendC(
		ctx, d.log,
		req.Resp, resp,
		"sending init chain response to Gordian",
	) {
		// If this failed it will have logged, so we can just return here.
		return false
	}

	d.log.Info(
		"Successfully sent init chain response to Gordian engine",
		"n_vals", len(resp.Validators),
		"app_state_hash", glog.Hex(resp.AppStateHash),
	)

	return true
}

func (d *Driver) mainLoop(
	ctx context.Context,
) {
	defer trace.StartRegion(ctx, "mainLoop").End()

	for {
		select {
		case <-ctx.Done():
			d.log.Info("Stopping due to context cancellation", "cause", context.Cause(ctx))
			return

		case req := <-d.finalizeBlockRequests:
			if !d.handleFinalization(ctx, req) {
				return
			}

		case ls := <-d.lagStateUpdates:
			if !d.handleLagStateUpdate(ctx, ls) {
				return
			}
		}
	}
}

func (d *Driver) handleFinalization(ctx context.Context, req tmdriver.FinalizeBlockRequest) bool {
	defer trace.StartRegion(ctx, "handleFinalization").End()

	// TODO: the comet implementation does some validation and checking for halt height and time,
	// which we are not yet doing.

	// TODO: don't hardcode the initial height.
	const initialHeight = 1
	if req.Header.Height == initialHeight {
		appHash, err := d.sdkStore.Commit(store.NewChangeset())
		if err != nil {
			d.log.Warn("Failed to commit new changeset for initial height", "err", err)
			return false
		}

		resp := tmdriver.FinalizeBlockResponse{
			Height:    req.Header.Height,
			Round:     req.Round,
			BlockHash: req.Header.Hash,

			// At genesis, we don't have a block response
			// from which to extract the next validators.
			// By design, the validators at height 2 match height 1.
			Validators:   req.Header.NextValidatorSet.Validators,
			AppStateHash: appHash,
		}
		if !gchan.SendC(
			ctx, d.log,
			req.Resp, resp,
			"sending finalize block response back to engine",
		) {
			// Context was cancelled and we already logged, so we're done.
			return false
		}

		return true
	}

	cID, err := d.sdkStore.LastCommitID()
	if err != nil {
		d.log.Warn(
			"Failed to get last commit ID prior to handling finalization request",
			"err", err,
		)
		return false
	}

	var ba BlockAnnotation
	if err := json.Unmarshal(req.Header.Annotations.Driver, &ba); err != nil {
		d.log.Warn(
			"Failed to extract driver annotation from finalize block request",
			"err", err,
		)
		return false
	}

	blockTime, err := ba.Time()
	if err != nil {
		d.log.Warn(
			"Failed to parse block time from driver annotation",
			"err", err,
		)
		return false
	}

	var txs []transaction.Tx

	// Guard the call to pool.Have with a zero check,
	// because we never call Need or SetAvailable on zero.
	if !gsbd.IsZeroTxDataID(string(req.Header.DataID)) {
		haveTxs, err, have := d.pool.Have(string(req.Header.DataID))
		if !have {
			// We need to make a blocking request to retrieve this data.
			panic(errors.New("TODO: handle block data not ready during finalization"))
		}
		if err != nil {
			panic(fmt.Errorf("TODO: handle error on retrieved block data during finalization: %w", err))
		}
		txs = haveTxs
	}

	blockReq := &coreserver.BlockRequest[transaction.Tx]{
		Height: req.Header.Height,

		Time: blockTime,

		Hash:    req.Header.Hash,
		AppHash: cID.Hash,
		ChainId: d.chainID,
		Txs:     txs,
	}

	// The app manager requires that comet info is set on its context
	// when dealing with a delivered block.
	ctx = context.WithValue(ctx, corecontext.CometInfoKey, corecomet.Info{
		// Somewhat surprisingly, just the presence of the key
		// without any values populated, is enough to progress past the panic.
	})

	// The discarded response value appears to include events
	// which we are not yet using.
	blockResp, newState, err := d.am.DeliverBlock(ctx, blockReq)
	if err != nil {
		d.log.Warn(
			"Failed to deliver block",
			"height", blockReq.Height,
			"err", err,
		)
		return false
	}

	// By default, we just use the block's declared next validators block.
	updatedVals := req.Header.NextValidatorSet.Validators
	if len(blockResp.ValidatorUpdates) > 0 {
		// We can never modify a ValdatorSet's validators,
		// so create a clone.
		updatedVals = slices.Clone(req.Header.NextValidatorSet.Validators)

		// Make a map of pubkeys that have a power change.
		// TODO: this doesn't respect key type, and it should.
		var valsToUpdate = make(map[string]uint64)
		hasDelete := false
		for _, vu := range blockResp.ValidatorUpdates {
			// TODO: vu.Power is an int64, and we are casting it to uint64 here.
			// There needs to be a safety check on conversion.
			valsToUpdate[string(vu.PubKey)] = uint64(vu.Power)

			if vu.Power == 0 {
				// Track whether we need to delete any.
				// We can avoid an extra iteration or two over the new validators
				// if we know nobody's power has dropped to zero.
				hasDelete = true
			}
		}

		// Now iterate over all the validators, applying the new powers.
		for i := range updatedVals {
			// Is the current validator in the update map?
			newPow, ok := valsToUpdate[string(updatedVals[i].PubKey.PubKeyBytes())]
			if !ok {
				continue
			}

			// Yes, so reassign its power.
			updatedVals[i].Power = newPow

			// Delete this entry.
			delete(valsToUpdate, string(updatedVals[i].PubKey.PubKeyBytes()))

			// Stop iterating if this was the last entry.
			if len(valsToUpdate) == 0 {
				break
			}
		}

		// If there were any zero powers, delete them first.
		// That might help avoid growing the slice if we delete some validators
		// before appending new ones.
		if hasDelete {
			updatedVals = slices.DeleteFunc(updatedVals, func(v tmconsensus.Validator) bool {
				return v.Power == 0
			})
		}

		if len(valsToUpdate) > 0 {
			// We have new validators!
			// They just go at the end for now,
			// which is probably not what we want long term.
			//
			// At least sort them by pubkey first so if multiple validators are added,
			// we ensure all participating validators agree on the order of the new ones.
			for _, pk := range slices.Sorted(maps.Keys(valsToUpdate)) {
				// Another dangerous int64->uint64 conversion.
				pow := uint64(valsToUpdate[pk])

				// Another poor assumption that we always use ed25519.
				pubKey, err := gcrypto.NewEd25519PubKey([]byte(pk))
				if err != nil {
					d.log.Warn(
						"Skipping new validator with invalid public key",
						"pub_key_bytes", glog.Hex(pk),
						"power", pow,
					)
					continue
				}

				updatedVals = append(updatedVals, tmconsensus.Validator{
					PubKey: pubKey, Power: pow,
				})
			}
		}
	}

	// Rebase the transactions on the new state.
	// For now, we just discard the invalidated transactions.
	// We could log them but it isn't clear what exactly should be logged.
	// Maybe the transaction hash would suffice?
	if _, err := d.txBuf.Rebase(ctx, newState, txs); err != nil {
		d.log.Warn("Failed to rebase transaction buffer", "err", err)
		return false
	}

	stateChanges, err := newState.GetStateChanges()
	if err != nil {
		d.log.Warn("Failed to get state changes", "err", err)
		return false
	}
	appHash, err := d.sdkStore.Commit(&store.Changeset{Changes: stateChanges})
	if err != nil {
		d.log.Warn("Failed to commit state changes", "err", err)
		return false
	}
	d.log.Info("Committed change to root store", "height", req.Header.Height, "apphash", glog.Hex(appHash))

	// TODO: There could be updated consensus params that we care about here.

	fbResp := tmdriver.FinalizeBlockResponse{
		Height:    req.Header.Height,
		Round:     req.Round,
		BlockHash: req.Header.Hash,

		Validators: updatedVals,

		AppStateHash: appHash,
	}
	if !gchan.SendC(
		ctx, d.log,
		req.Resp, fbResp,
		"sending finalize block response back to engine",
	) {
		// Context was cancelled and we already logged, so we're done.
		return false
	}

	return true
}

func (d *Driver) handleLagStateUpdate(ctx context.Context, ls tmelink.LagState) bool {
	defer trace.StartRegion(ctx, "handleLagStateUpdate").End()

	switch ls.Status {
	case tmelink.LagStatusInitializing,
		tmelink.LagStatusAssumedBehind,
		tmelink.LagStatusKnownMissing:
		if !d.sClient.ResumeFetching(ctx, ls.CommittingHeight+1, ls.NeedHeight) {
			return false
		}
	case tmelink.LagStatusUpToDate:
		if !d.sClient.PauseFetching(ctx) {
			return false
		}
	default:
		panic(fmt.Errorf(
			"BUG: received unknown lag status %q", ls.Status,
		))
	}

	return true
}

func (d *Driver) Wait() {
	<-d.done
}
