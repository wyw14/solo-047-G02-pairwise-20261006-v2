package evidence

import (
	"testing"
	"time"

	"github.com/wyw14/cry-103/internal/landing"
	"github.com/wyw14/cry-103/internal/otdr"
	"github.com/wyw14/cry-103/internal/span"
	"github.com/wyw14/cry-103/internal/topology"
)

func newLedger() *Ledger {
	base := time.Date(2026, 10, 6, 8, 0, 0, 0, time.UTC)
	return NewLedger(DefaultThresholds(), func() time.Time { return base })
}

func chunk(station string, index int, gen uint64, distance, loss float64) landing.TraceChunk {
	return landing.TraceChunk{
		StationID: station, Index: index, Path: topology.PathPrimary,
		Generation: gen, DistanceKM: distance, LossDB: loss,
	}
}

func buildTrace(t *testing.T, id, spanID, station string, gen uint64, chunks []landing.TraceChunk, completed time.Time) otdr.Trace {
	t.Helper()
	reflections := make([]span.Reflection, 0, len(chunks))
	for _, item := range chunks {
		reflections = append(reflections, span.Reflection{DistanceKM: item.DistanceKM, LossDB: item.LossDB, Direction: item.StationID})
	}
	location, err := span.Locate(200, reflections)
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	return otdr.Trace{
		ID: id, SpanID: spanID, Generation: gen, Path: topology.PathPrimary,
		Chunks: chunks, Location: location,
		StartedAt: completed.Add(-time.Second), CompletedAt: completed,
	}
}

func hasKind(annotations []Annotation, kind AnnotationKind) bool {
	for _, annotation := range annotations {
		if annotation.Kind == kind {
			return true
		}
	}
	return false
}

func TestAnalyzeMarksBreakLossStepAndInsufficient(t *testing.T) {
	thresholds := DefaultThresholds()
	trace := otdr.Trace{Generation: 1, Chunks: []landing.TraceChunk{
		{StationID: "east", Index: 0, DistanceKM: 10, LossDB: 2.0},
		{StationID: "east", Index: 1, DistanceKM: 20, LossDB: 4.0},
		{StationID: "east", Index: 2, DistanceKM: 30, LossDB: 9.0},
	}}
	segments, annotations := analyze(trace, thresholds)
	if len(segments) != 3 {
		t.Fatalf("expected 3 segments, got %d", len(segments))
	}
	if segments[0].Kind != SegmentAttenuation || segments[1].Kind != SegmentLossStep || segments[2].Kind != SegmentBreak {
		t.Fatalf("unexpected segment kinds: %+v", segments)
	}
	if !hasKind(annotations, AnnotationBreak) || !hasKind(annotations, AnnotationLossStep) {
		t.Fatalf("expected break and loss-step annotations, got %+v", annotations)
	}
	if hasKind(annotations, AnnotationInsufficient) {
		t.Fatal("three-point curve must not be marked insufficient")
	}

	short := otdr.Trace{Generation: 1, Chunks: []landing.TraceChunk{
		{StationID: "east", Index: 0, DistanceKM: 10, LossDB: 2.0},
	}}
	if _, annotations := analyze(short, thresholds); !hasKind(annotations, AnnotationInsufficient) {
		t.Fatalf("single-point curve must be marked insufficient, got %+v", annotations)
	}
}

func TestIngestRejectsCrossGenerationChunk(t *testing.T) {
	ledger := newLedger()
	trace := buildTrace(t, "scan-x", "S3", "east", 1, []landing.TraceChunk{
		chunk("east", 0, 1, 10, 2), chunk("east", 1, 2, 20, 3),
	}, time.Date(2026, 10, 6, 8, 0, 0, 0, time.UTC))
	if _, err := ledger.Ingest(trace); err == nil {
		t.Fatal("scan mixing chunk generations must be rejected")
	}
}

func TestGenerationsStayInSeparateChainsAndCorroborate(t *testing.T) {
	ledger := newLedger()
	t1 := time.Date(2026, 10, 6, 8, 0, 0, 0, time.UTC)

	east := buildTrace(t, "scan-east", "S3", "east", 1, []landing.TraceChunk{
		chunk("east", 0, 1, 10, 2), chunk("east", 1, 1, 30, 4), chunk("east", 2, 1, 50, 9),
	}, t1)
	west := buildTrace(t, "scan-west", "S3", "west", 1, []landing.TraceChunk{
		chunk("west", 0, 1, 12, 2), chunk("west", 1, 1, 32, 4), chunk("west", 2, 1, 50.5, 9),
	}, t1.Add(time.Minute))
	if _, err := ledger.Ingest(east); err != nil {
		t.Fatal(err)
	}
	gen1, exists := ledger.Chain("S3", 1)
	if !exists {
		t.Fatal("generation-1 chain missing")
	}
	if len(gen1.Stations) != 1 || gen1.EvidenceSufficient {
		t.Fatalf("single-station chain with a break but no peer/proofs must be insufficient: %+v", gen1)
	}
	if _, err := ledger.Ingest(west); err != nil {
		t.Fatal(err)
	}
	gen1, _ = ledger.Chain("S3", 1)
	if len(gen1.Stations) != 2 {
		t.Fatalf("expected two stations, got %d", len(gen1.Stations))
	}
	for _, station := range gen1.Stations {
		if !station.DistanceAgrees || station.DistanceSpreadKM > 2 {
			t.Fatalf("stations must corroborate within 2 km: %+v", station)
		}
	}
	if !gen1.EvidenceSufficient {
		t.Fatalf("two-station corroboration of a break is sufficient, reasons: %v", gen1.InsufficientReasons)
	}

	// Generation 2 (post-switch) must form its own chain and never merge into gen 1.
	gen2Chunks := []landing.TraceChunk{
		{StationID: "east", Index: 0, Path: topology.PathProtection, Generation: 2, DistanceKM: 60, LossDB: 9},
		{StationID: "east", Index: 1, Path: topology.PathProtection, Generation: 2, DistanceKM: 80, LossDB: 9.5},
	}
	east2 := buildTrace(t, "scan-east-2", "S3", "east", 2, gen2Chunks, t1.Add(2*time.Hour))
	east2.Path = topology.PathProtection
	if _, err := ledger.Ingest(east2); err != nil {
		t.Fatal(err)
	}
	chains := ledger.ForSpan("S3", time.Time{}, time.Time{}, 0)
	if len(chains) != 2 || chains[0].Generation != 1 || chains[1].Generation != 2 {
		t.Fatalf("expected two ordered, separate generation chains, got %+v", chains)
	}
	gen2, _ := ledger.Chain("S3", 2)
	if len(gen2.Stations) != 1 || gen2.EvidenceSufficient {
		t.Fatalf("generation 2 must be a one-station, insufficient chain: %+v", gen2)
	}
}

func TestTimeWindowAndGenerationFilter(t *testing.T) {
	ledger := newLedger()
	t1 := time.Date(2026, 10, 6, 8, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	if _, err := ledger.Ingest(buildTrace(t, "scan-g1", "S3", "east", 1,
		[]landing.TraceChunk{chunk("east", 0, 1, 10, 9), chunk("east", 1, 1, 20, 9.5)}, t1)); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Ingest(buildTrace(t, "scan-g2", "S3", "east", 2,
		[]landing.TraceChunk{chunk("east", 0, 2, 10, 9), chunk("east", 1, 2, 20, 9.5)}, t2)); err != nil {
		t.Fatal(err)
	}
	windowed := ledger.ForSpan("S3", t1.Add(-time.Minute), t1.Add(time.Minute), 0)
	if len(windowed) != 1 || windowed[0].Generation != 1 {
		t.Fatalf("window around t1 must return only generation 1, got %+v", windowed)
	}
	onlyGen2 := ledger.ForSpan("S3", time.Time{}, time.Time{}, 2)
	if len(onlyGen2) != 1 || onlyGen2[0].Generation != 2 {
		t.Fatalf("generation filter must return only generation 2, got %+v", onlyGen2)
	}
}

func TestProofsAreAnchoredToObservedGenerations(t *testing.T) {
	ledger := newLedger()
	at := time.Date(2026, 10, 6, 8, 0, 0, 0, time.UTC)
	// A proof arriving before any scan opens an insufficient chain, but never errors.
	premature, err := ledger.AddProof("S3", 1, otdr.Proof{Direction: otdr.DirectionEastToWest, Passed: true})
	if err != nil {
		t.Fatalf("early proof should open a chain, got error: %v", err)
	}
	if premature.EvidenceSufficient {
		t.Fatal("a proof with no raw scan must leave the chain insufficient")
	}
	// Healthy post-repair scan: no break or loss step, single station.
	healthy := buildTrace(t, "scan-ok", "S3", "east", 1,
		[]landing.TraceChunk{chunk("east", 0, 1, 10, 2.0), chunk("east", 1, 1, 20, 2.4), chunk("east", 2, 1, 30, 2.8)}, at)
	if _, err := ledger.Ingest(healthy); err != nil {
		t.Fatal(err)
	}
	chain, err := ledger.AddProof("S3", 1, otdr.Proof{ID: "p2", Direction: otdr.DirectionWestToEast, Passed: true, LossDB: 0.5, MeasuredAt: at})
	if err != nil {
		t.Fatal(err)
	}
	if !chain.ProofResult.Complete || !chain.ProofResult.Passed || !chain.EvidenceSufficient {
		t.Fatalf("healthy scan with complete bidirectional proof must be sufficient: %+v reasons %v", chain.ProofResult, chain.InsufficientReasons)
	}
	// A generation-2 proof opens a separate chain and must not complete generation 1.
	if _, err := ledger.AddProof("S3", 2, otdr.Proof{Direction: otdr.DirectionEastToWest, Passed: true}); err != nil {
		t.Fatal(err)
	}
	gen1, _ := ledger.Chain("S3", 1)
	gen2, _ := ledger.Chain("S3", 2)
	if len(gen1.Proofs) != 2 || len(gen2.Proofs) != 1 || gen2.EvidenceSufficient {
		t.Fatalf("proofs must stay inside their own generation: gen1=%d gen2=%d", len(gen1.Proofs), len(gen2.Proofs))
	}
}

func TestFailingProofMarksInsufficient(t *testing.T) {
	ledger := newLedger()
	at := time.Date(2026, 10, 6, 8, 0, 0, 0, time.UTC)
	if _, err := ledger.Ingest(buildTrace(t, "scan-ok", "S3", "east", 1,
		[]landing.TraceChunk{chunk("east", 0, 1, 10, 2.0), chunk("east", 1, 1, 20, 2.4)}, at)); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.AddProof("S3", 1, otdr.Proof{ID: "p1", Direction: otdr.DirectionEastToWest, Passed: true, MeasuredAt: at}); err != nil {
		t.Fatal(err)
	}
	chain, err := ledger.AddProof("S3", 1, otdr.Proof{ID: "p2", Direction: otdr.DirectionWestToEast, Passed: false, MeasuredAt: at})
	if err != nil {
		t.Fatal(err)
	}
	if chain.EvidenceSufficient {
		t.Fatal("a failed directional proof must keep the chain insufficient")
	}
}

func TestIncidentRetrievalStaysInsideBoundGeneration(t *testing.T) {
	ledger := newLedger()
	t1 := time.Date(2026, 10, 6, 8, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	if _, err := ledger.Ingest(buildTrace(t, "scan-g1", "S3", "east", 1,
		[]landing.TraceChunk{chunk("east", 0, 1, 10, 9), chunk("east", 1, 1, 20, 9.5)}, t1)); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Ingest(buildTrace(t, "scan-g2", "S3", "east", 2,
		[]landing.TraceChunk{chunk("east", 0, 2, 10, 9), chunk("east", 1, 2, 20, 9.5)}, t2)); err != nil {
		t.Fatal(err)
	}
	if err := ledger.BindIncident("INC-1", "S3", 1); err != nil {
		t.Fatal(err)
	}
	if err := ledger.BindIncident("INC-1", "S3", 2); err == nil {
		t.Fatal("rebinding an incident to another generation must fail")
	}
	if _, err := ledger.ForIncident("INC-9", time.Time{}, time.Time{}); err == nil {
		t.Fatal("unbound incident retrieval must fail")
	}
	bundle, err := ledger.ForIncident("INC-1", time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Generations) != 1 || bundle.Generations[0].Generation != 1 {
		t.Fatalf("incident evidence must stay in generation 1, got %+v", bundle.Generations)
	}
}

func TestDisagreeingStationDistancesAreInsufficient(t *testing.T) {
	ledger := newLedger()
	at := time.Date(2026, 10, 6, 8, 0, 0, 0, time.UTC)
	if _, err := ledger.Ingest(buildTrace(t, "east", "S3", "east", 1,
		[]landing.TraceChunk{chunk("east", 0, 1, 10, 4), chunk("east", 1, 1, 40, 9)}, at)); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Ingest(buildTrace(t, "west", "S3", "west", 1,
		[]landing.TraceChunk{chunk("west", 0, 1, 10, 4), chunk("west", 1, 1, 60, 9)}, at.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	chain, _ := ledger.Chain("S3", 1)
	for _, station := range chain.Stations {
		if station.DistanceAgrees {
			t.Fatalf("stations 20 km apart must not corroborate: %+v", station)
		}
	}
	if chain.EvidenceSufficient {
		t.Fatalf("disagreeing stations with no proofs must be insufficient, reasons: %v", chain.InsufficientReasons)
	}
}
