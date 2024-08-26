package tmmemstore

import (
	"context"
	"sync"

	"github.com/rollchains/gordian/gcrypto"
	"github.com/rollchains/gordian/tm/tmconsensus"
	"github.com/rollchains/gordian/tm/tmstore"
)

type hr struct {
	H uint64
	R uint32
}

type ActionStore struct {
	mu sync.RWMutex

	ras map[hr]tmstore.RoundActions
}

func NewActionStore() *ActionStore {
	return &ActionStore{
		ras: make(map[hr]tmstore.RoundActions),
	}
}

func (s *ActionStore) SaveProposedHeader(ctx context.Context, ph tmconsensus.ProposedHeader) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	hr := hr{H: ph.Header.Height, R: ph.Round}
	ra, ok := s.ras[hr]
	if ok && ra.ProposedHeader.Header.Height != 0 {
		return tmstore.DoubleActionError{Type: "proposed block"}
	}

	ra.Height = hr.H
	ra.Round = hr.R
	ra.ProposedHeader = ph

	s.ras[hr] = ra
	return nil
}

func (s *ActionStore) SavePrevote(ctx context.Context, pubKey gcrypto.PubKey, vt tmconsensus.VoteTarget, sig []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	hr := hr{H: vt.Height, R: vt.Round}
	ra, ok := s.ras[hr]
	if ok {
		if ra.PrevoteSignature != "" {
			return tmstore.DoubleActionError{Type: "prevote"}
		}

		if ra.PubKey != nil && !ra.PubKey.Equal(pubKey) {
			return tmstore.PubKeyChangedError{
				ActionType: "prevote",
				Want:       string(ra.PubKey.PubKeyBytes()),
				Got:        string(pubKey.PubKeyBytes()),
			}
		}
	}

	ra.Height = hr.H
	ra.Round = hr.R

	ra.PrevoteTarget = vt.BlockHash
	ra.PrevoteSignature = string(sig)
	ra.PubKey = pubKey

	s.ras[hr] = ra
	return nil
}

func (s *ActionStore) SavePrecommit(ctx context.Context, pubKey gcrypto.PubKey, vt tmconsensus.VoteTarget, sig []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	hr := hr{H: vt.Height, R: vt.Round}
	ra, ok := s.ras[hr]
	if ok {
		if ra.PrecommitSignature != "" {
			return tmstore.DoubleActionError{Type: "precommit"}
		}

		if ra.PubKey != nil && !ra.PubKey.Equal(pubKey) {
			return tmstore.PubKeyChangedError{
				ActionType: "precommit",
				Want:       string(ra.PubKey.PubKeyBytes()),
				Got:        string(pubKey.PubKeyBytes()),
			}
		}
	}

	ra.Height = hr.H
	ra.Round = hr.R

	ra.PrecommitTarget = vt.BlockHash
	ra.PrecommitSignature = string(sig)
	ra.PubKey = pubKey

	s.ras[hr] = ra
	return nil
}

// Load returns all actions recorded for this round.
func (s *ActionStore) Load(ctx context.Context, height uint64, round uint32) (tmstore.RoundActions, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	hr := hr{H: height, R: round}
	ra, ok := s.ras[hr]
	if !ok {
		return tmstore.RoundActions{}, tmconsensus.RoundUnknownError{
			WantHeight: height,
			WantRound:  round,
		}
	}

	return ra, nil
}
