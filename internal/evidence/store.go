package evidence

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/wyw14/cry-103/internal/otdr"
	"github.com/wyw14/cry-103/internal/platform"
)

type spanGen struct {
	spanID     string
	generation uint64
}

type Store struct {
	mu         sync.RWMutex
	thresholds Thresholds
	ids        platform.IDGenerator
	chains     map[string]Chain
	order      map[string][]string
	proofs     map[spanGen]map[otdr.Direction]ProofLink
}

func NewStore(thresholds Thresholds, ids platform.IDGenerator) *Store {
	return &Store{
		thresholds: thresholds, ids: ids,
		chains: make(map[string]Chain), order: make(map[string][]string),
		proofs: make(map[spanGen]map[otdr.Direction]ProofLink),
	}
}

func (s *Store) AddTrace(trace otdr.Trace) (Chain, error) {
	if len(trace.Generations()) != 1 {
		return Chain{}, errors.New("evidence chain refuses a trace that crosses topology generations")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := spanGen{spanID: trace.SpanID, generation: trace.Generation}
	segments, findings := analyzeCurve(trace, s.thresholds)
	proofs := s.poolLocked(key)
	chain := Chain{
		ID: s.ids.New("chain"), SpanID: trace.SpanID, Generation: trace.Generation,
		Path: trace.Path, TraceID: trace.ID, StationID: trace.Chunks[0].StationID,
		StartedAt: trace.StartedAt, CompletedAt: trace.CompletedAt,
		Confidence: trace.Location.Confidence,
		Segments:   segments, Proofs: proofs,
	}
	chain.Findings = append(findings, sufficiency(trace.Location.Confidence, segments, proofs, s.thresholds)...)
	chain.Sufficient = !hasInsufficient(chain.Findings)
	s.chains[chain.ID] = chain
	s.order[chain.SpanID] = append(s.order[chain.SpanID], chain.ID)
	return chain, nil
}

func (s *Store) LinkProof(spanID string, generation uint64, proof otdr.Proof) ([]Chain, error) {
	if proof.Direction != otdr.DirectionEastToWest && proof.Direction != otdr.DirectionWestToEast {
		return nil, fmt.Errorf("unsupported proof direction %q", proof.Direction)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := spanGen{spanID: spanID, generation: generation}
	pool := s.proofs[key]
	if pool == nil {
		pool = make(map[otdr.Direction]ProofLink)
		s.proofs[key] = pool
	}
	pool[proof.Direction] = ProofLink{
		ProofID: proof.ID, Direction: proof.Direction, Passed: proof.Passed,
		LossDB: proof.LossDB, MeasuredAt: proof.MeasuredAt,
	}
	proofs := s.poolLocked(key)
	updated := make([]Chain, 0)
	for _, id := range s.order[spanID] {
		chain := s.chains[id]
		if chain.Generation != generation {
			continue
		}
		chain.Proofs = proofs
		kept := chain.Findings[:0]
		for _, finding := range chain.Findings {
			if finding.Kind != FindingInsufficient {
				kept = append(kept, finding)
			}
		}
		chain.Findings = append(kept, sufficiency(chain.Confidence, chain.Segments, proofs, s.thresholds)...)
		chain.Sufficient = !hasInsufficient(chain.Findings)
		s.chains[id] = chain
		updated = append(updated, chain)
	}
	return updated, nil
}

func (s *Store) Get(id string) (Chain, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	chain, exists := s.chains[id]
	return chain, exists
}

func (s *Store) List(spanID string) []Chain {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Chain, 0, len(s.order[spanID]))
	for _, id := range s.order[spanID] {
		result = append(result, s.chains[id])
	}
	return result
}

// Query returns every chain of the span whose [StartedAt, CompletedAt] window
// overlaps [from, to], grouped into per-generation slices. A zero from/to
// leaves that end of the window open; generation > 0 pins one slice.
func (s *Store) Query(spanID string, from, to time.Time, generation uint64) Bundle {
	s.mu.RLock()
	defer s.mu.RUnlock()
	bundle := Bundle{SpanID: spanID, From: from.UTC(), To: to.UTC(), Generations: []GenerationSlice{}}
	byGeneration := make(map[uint64][]Chain)
	for _, id := range s.order[spanID] {
		chain := s.chains[id]
		if generation != 0 && chain.Generation != generation {
			continue
		}
		if !from.IsZero() && chain.CompletedAt.Before(from) {
			continue
		}
		if !to.IsZero() && chain.StartedAt.After(to) {
			continue
		}
		byGeneration[chain.Generation] = append(byGeneration[chain.Generation], chain)
	}
	generations := make([]uint64, 0, len(byGeneration))
	for value := range byGeneration {
		generations = append(generations, value)
	}
	sort.Slice(generations, func(i, j int) bool { return generations[i] < generations[j] })
	for _, value := range generations {
		bundle.Generations = append(bundle.Generations, GenerationSlice{Generation: value, Chains: byGeneration[value]})
	}
	return bundle
}

func (s *Store) poolLocked(key spanGen) []ProofLink {
	pool := s.proofs[key]
	proofs := make([]ProofLink, 0, len(pool))
	for _, direction := range []otdr.Direction{otdr.DirectionEastToWest, otdr.DirectionWestToEast} {
		if proof, exists := pool[direction]; exists {
			proofs = append(proofs, proof)
		}
	}
	return proofs
}

func hasInsufficient(findings []Finding) bool {
	for _, finding := range findings {
		if finding.Kind == FindingInsufficient {
			return true
		}
	}
	return false
}
