package evidence

import (
	"testing"
	"time"

	"github.com/wyw14/cry-103/internal/landing"
	"github.com/wyw14/cry-103/internal/otdr"
	"github.com/wyw14/cry-103/internal/platform"
	"github.com/wyw14/cry-103/internal/span"
	"github.com/wyw14/cry-103/internal/topology"
)

func testTrace(generation uint64, losses []float64, confidence float64, completedAt time.Time) otdr.Trace {
	chunks := make([]landing.TraceChunk, 0, len(losses))
	for index, loss := range losses {
		chunks = append(chunks, landing.TraceChunk{
			StationID: "east", Index: index, Path: topology.PathPrimary,
			Generation: generation, DistanceKM: 18 + float64(index)*3.5, LossDB: loss,
		})
	}
	return otdr.Trace{
		ID: "trace-1", SpanID: "S3", Generation: generation, Path: topology.PathPrimary,
		Chunks: chunks, Location: span.FaultLocation{DistanceKM: 18, Confidence: confidence},
		StartedAt: completedAt.Add(-time.Minute), CompletedAt: completedAt,
	}
}

func newStore() *Store {
	return NewStore(DefaultThresholds(), platform.UUIDGenerator{})
}

func findKinds(chain Chain) map[FindingKind]int {
	counts := make(map[FindingKind]int)
	for _, finding := range chain.Findings {
		counts[finding.Kind]++
	}
	return counts
}

func TestAnalyzeFlagsBreakpointAndLossStep(t *testing.T) {
	store := newStore()
	trace := testTrace(1, []float64{1.0, 1.4, 7.0, 8.3}, 0.9, time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC))
	chain, err := store.AddTrace(trace)
	if err != nil {
		t.Fatal(err)
	}
	if len(chain.Segments) != 3 {
		t.Fatalf("expected 3 segments, got %d", len(chain.Segments))
	}
	counts := findKinds(chain)
	if counts[FindingBreakpoint] != 1 {
		t.Fatalf("expected one breakpoint finding, got %+v", chain.Findings)
	}
	if counts[FindingLossStep] != 1 {
		t.Fatalf("expected one loss-step finding, got %+v", chain.Findings)
	}
	var breakpoint Finding
	for _, finding := range chain.Findings {
		if finding.Kind == FindingBreakpoint {
			breakpoint = finding
		}
	}
	if breakpoint.DistanceKM != 25 {
		t.Fatalf("breakpoint should anchor at 25 km, got %+v", breakpoint)
	}
}

func TestAddTraceRejectsCrossGeneration(t *testing.T) {
	store := newStore()
	trace := testTrace(1, []float64{1.0, 1.5, 2.0}, 0.9, time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC))
	trace.Chunks[1].Generation = 2
	if _, err := store.AddTrace(trace); err == nil {
		t.Fatal("a trace crossing topology generations must be rejected")
	}
}

func TestInsufficientEvidenceFlags(t *testing.T) {
	store := newStore()
	trace := testTrace(1, []float64{1.0, 1.2}, 0.3, time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC))
	chain, err := store.AddTrace(trace)
	if err != nil {
		t.Fatal(err)
	}
	if chain.Sufficient {
		t.Fatal("thin curve with low confidence and no proofs must be insufficient")
	}
	counts := findKinds(chain)
	if counts[FindingInsufficient] != 4 {
		t.Fatalf("expected segment, confidence, and two direction gaps, got %+v", chain.Findings)
	}
}

func TestLinkProofStaysInsideGeneration(t *testing.T) {
	store := newStore()
	at := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	gen1, err := store.AddTrace(testTrace(1, []float64{1.0, 1.5, 2.0}, 0.9, at))
	if err != nil {
		t.Fatal(err)
	}
	gen2, err := store.AddTrace(testTrace(2, []float64{1.0, 1.5, 2.0}, 0.9, at.Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	updated, err := store.LinkProof("S3", 1, otdr.Proof{ID: "p1", Direction: otdr.DirectionEastToWest, Passed: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LinkProof("S3", 1, otdr.Proof{ID: "p2", Direction: otdr.DirectionWestToEast, Passed: true}); err != nil {
		t.Fatal(err)
	}
	if len(updated) != 1 || updated[0].ID != gen1.ID {
		t.Fatalf("only the generation-1 chain may be updated, got %+v", updated)
	}
	chain1, _ := store.Get(gen1.ID)
	if !chain1.Sufficient || len(chain1.Proofs) != 2 {
		t.Fatalf("generation-1 chain should be sufficient with both proofs, got %+v", chain1)
	}
	chain2, _ := store.Get(gen2.ID)
	if len(chain2.Proofs) != 0 || chain2.Sufficient {
		t.Fatalf("generation-2 chain must stay untouched by generation-1 proofs, got %+v", chain2)
	}
}

func TestFailedProofKeepsChainInsufficient(t *testing.T) {
	store := newStore()
	at := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	chain, err := store.AddTrace(testTrace(1, []float64{1.0, 1.5, 2.0}, 0.9, at))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LinkProof("S3", 1, otdr.Proof{ID: "p1", Direction: otdr.DirectionEastToWest, Passed: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LinkProof("S3", 1, otdr.Proof{ID: "p2", Direction: otdr.DirectionWestToEast, Passed: false}); err != nil {
		t.Fatal(err)
	}
	chain, _ = store.Get(chain.ID)
	if chain.Sufficient {
		t.Fatal("a failed directional proof must keep the chain insufficient")
	}
}

func TestQueryGroupsByGenerationAndWindow(t *testing.T) {
	store := newStore()
	base := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	if _, err := store.AddTrace(testTrace(1, []float64{1.0, 1.5, 2.0}, 0.9, base)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddTrace(testTrace(1, []float64{1.1, 1.6, 2.1}, 0.9, base.Add(2*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddTrace(testTrace(2, []float64{1.2, 1.7, 2.2}, 0.9, base.Add(4*time.Hour))); err != nil {
		t.Fatal(err)
	}
	bundle := store.Query("S3", time.Time{}, time.Time{}, 0)
	if len(bundle.Generations) != 2 {
		t.Fatalf("expected two generation slices, got %+v", bundle.Generations)
	}
	if bundle.Generations[0].Generation != 1 || len(bundle.Generations[0].Chains) != 2 {
		t.Fatalf("generation 1 slice must hold two chains, got %+v", bundle.Generations[0])
	}
	if bundle.Generations[1].Generation != 2 || len(bundle.Generations[1].Chains) != 1 {
		t.Fatalf("generation 2 slice must hold one chain, got %+v", bundle.Generations[1])
	}
	windowed := store.Query("S3", base.Add(90*time.Minute), base.Add(3*time.Hour), 0)
	if len(windowed.Generations) != 1 || len(windowed.Generations[0].Chains) != 1 {
		t.Fatalf("time window must select only the middle chain, got %+v", windowed.Generations)
	}
	pinned := store.Query("S3", time.Time{}, time.Time{}, 2)
	if len(pinned.Generations) != 1 || pinned.Generations[0].Generation != 2 {
		t.Fatalf("generation pin must return only generation 2, got %+v", pinned.Generations)
	}
	if other := store.Query("S9", time.Time{}, time.Time{}, 0); len(other.Generations) != 0 {
		t.Fatalf("unknown span must return an empty bundle, got %+v", other.Generations)
	}
}
