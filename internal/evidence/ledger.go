package evidence

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/wyw14/cry-103/internal/otdr"
)

type chainKey struct {
	SpanID     string
	Generation uint64
}

type incidentBinding struct {
	SpanID     string
	Generation uint64
}

// Ledger is the fibre-test evidence chain store. Every scan and proof is
// filed under an explicit (span, topology-generation) key, so data measured
// against different topology generations can never be mixed into one chain.
type Ledger struct {
	mu         sync.RWMutex
	thresholds Thresholds
	now        func() time.Time
	records    map[string]ScanRecord
	chains     map[chainKey]*GenerationChain
	incidents  map[string]incidentBinding
}

func NewLedger(thresholds Thresholds, now func() time.Time) *Ledger {
	return &Ledger{
		thresholds: thresholds, now: now,
		records:   make(map[string]ScanRecord),
		chains:    make(map[chainKey]*GenerationChain),
		incidents: make(map[string]incidentBinding),
	}
}

// Ingest pins a completed OTDR scan to its topology generation, derives the
// segmented curve and annotations, and files it in the matching chain.
func (l *Ledger) Ingest(trace otdr.Trace) (ScanRecord, error) {
	if trace.ID == "" {
		return ScanRecord{}, fmt.Errorf("scan trace identity is required")
	}
	if trace.SpanID == "" || trace.Generation == 0 {
		return ScanRecord{}, fmt.Errorf("scan %s is missing span or topology generation", trace.ID)
	}
	for _, chunk := range trace.Chunks {
		if chunk.Generation != trace.Generation {
			return ScanRecord{}, fmt.Errorf("scan %s chunk generation %d does not match pinned generation %d", trace.ID, chunk.Generation, trace.Generation)
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if existing, exists := l.records[trace.ID]; exists {
		if existing.Generation != trace.Generation || existing.SpanID != trace.SpanID {
			return ScanRecord{}, fmt.Errorf("scan %s re-ingested under a different span or generation", trace.ID)
		}
		return existing, nil
	}
	segments, annotations := analyze(trace, l.thresholds)
	record := ScanRecord{
		Trace: trace, StationID: chunksStation(trace.Chunks), Segments: segments,
		Annotations: annotations, RecordedAt: recordedAt(trace, l.now),
	}
	l.records[trace.ID] = record
	key := chainKey{SpanID: trace.SpanID, Generation: trace.Generation}
	chain := l.chains[key]
	if chain == nil {
		chain = &GenerationChain{SpanID: trace.SpanID, Generation: trace.Generation, Path: trace.Path}
		l.chains[key] = chain
	}
	l.rebuildLocked(chain)
	return record, nil
}

// AddProof files a directional optical proof against one generation. Proofs of
// different generations stay in different chains and can never complete each
// other. If no scan of that generation exists yet the chain is opened anyway
// and flagged insufficient ("no scan recorded") until raw scans arrive.
func (l *Ledger) AddProof(spanID string, generation uint64, proof otdr.Proof) (GenerationChain, error) {
	if spanID == "" || generation == 0 {
		return GenerationChain{}, fmt.Errorf("span and topology generation are required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	key := chainKey{SpanID: spanID, Generation: generation}
	chain := l.chains[key]
	if chain == nil {
		chain = &GenerationChain{SpanID: spanID, Generation: generation}
		l.chains[key] = chain
	}
	replaced := false
	for index := range chain.Proofs {
		if chain.Proofs[index].Direction == proof.Direction {
			chain.Proofs[index] = DirectionalProof{Proof: proof, Generation: generation, Direction: proof.Direction}
			replaced = true
			break
		}
	}
	if !replaced {
		chain.Proofs = append(chain.Proofs, DirectionalProof{Proof: proof, Generation: generation, Direction: proof.Direction})
	}
	l.rebuildLocked(chain)
	return *chain, nil
}

// BindIncident ties an incident to the generation it was raised against. All
// later evidence retrieval for that incident stays inside that generation.
func (l *Ledger) BindIncident(incidentID, spanID string, generation uint64) error {
	if incidentID == "" || spanID == "" || generation == 0 {
		return fmt.Errorf("incident, span and topology generation are required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if existing, exists := l.incidents[incidentID]; exists && (existing.SpanID != spanID || existing.Generation != generation) {
		return fmt.Errorf("incident %s already bound to span %s generation %d", incidentID, existing.SpanID, existing.Generation)
	}
	l.incidents[incidentID] = incidentBinding{SpanID: spanID, Generation: generation}
	return nil
}

// IncidentBinding reports the span and generation an incident is pinned to.
func (l *Ledger) IncidentBinding(incidentID string) (string, uint64, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	binding, exists := l.incidents[incidentID]
	if !exists {
		return "", 0, false
	}
	return binding.SpanID, binding.Generation, true
}

// Record returns the filed scan record for a trace ID.
func (l *Ledger) Record(traceID string) (ScanRecord, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	record, exists := l.records[traceID]
	return record, exists
}

// Chain returns one generation's evidence chain.
func (l *Ledger) Chain(spanID string, generation uint64) (GenerationChain, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	chain, exists := l.chains[chainKey{SpanID: spanID, Generation: generation}]
	if !exists {
		return GenerationChain{}, false
	}
	return *chain, true
}

// ForSpan returns every generation chain of a span whose scans fall inside the
// time window. Chains stay separate: callers never receive merged generations.
// onlyGeneration > 0 restricts the result to that single generation.
func (l *Ledger) ForSpan(spanID string, from, to time.Time, onlyGeneration uint64) []GenerationChain {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var result []GenerationChain
	for key, chain := range l.chains {
		if key.SpanID != spanID {
			continue
		}
		if onlyGeneration > 0 && key.Generation != onlyGeneration {
			continue
		}
		if !chainHasScanInWindow(chain, from, to) {
			continue
		}
		result = append(result, *chain)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Generation < result[j].Generation })
	return result
}

// ForIncident retrieves the raw evidence for an incident, restricted to the
// line and time window and — critically — to the incident's bound generation,
// so a later post-repair generation can never contaminate the incident.
func (l *Ledger) ForIncident(incidentID string, from, to time.Time) (IncidentEvidence, error) {
	l.mu.RLock()
	binding, exists := l.incidents[incidentID]
	l.mu.RUnlock()
	if !exists {
		return IncidentEvidence{}, fmt.Errorf("incident %s is not bound to a topology generation", incidentID)
	}
	chains := l.ForSpan(binding.SpanID, from, to, binding.Generation)
	return IncidentEvidence{
		SpanID: binding.SpanID, IncidentID: incidentID,
		From: from.UTC(), To: to.UTC(), Generations: chains, CurrentGeneration: binding.Generation,
	}, nil
}

func (l *Ledger) rebuildLocked(chain *GenerationChain) {
	byStation := make(map[string][]ScanRecord)
	var scanIDs []string
	annotationSeen := make(map[string]bool)
	chain.Annotations = nil
	for _, record := range l.records {
		if record.SpanID != chain.SpanID || record.Generation != chain.Generation {
			continue
		}
		scanIDs = append(scanIDs, record.Trace.ID)
		byStation[record.StationID] = append(byStation[record.StationID], record)
		for _, annotation := range record.Annotations {
			key := fmt.Sprintf("%s@%.4f", annotation.Kind, annotation.DistanceKM)
			if !annotationSeen[key] {
				annotationSeen[key] = true
				chain.Annotations = append(chain.Annotations, annotation)
			}
		}
	}
	sort.Strings(scanIDs)
	chain.ScanIDs = scanIDs
	chain.Scans = orderedScans(l.records, scanIDs)
	if len(chain.Scans) > 0 && chain.Path == "" {
		chain.Path = chain.Scans[0].Path
	}
	chain.Stations = buildStations(byStation, l.thresholds)
	chain.ProofResult = rebuildProofResult(chain.Proofs)
	chain.EvidenceSufficient, chain.InsufficientReasons = assess(chain, l.thresholds)
	chain.UpdatedAt = l.now().UTC()
}

func rebuildProofResult(proofs []DirectionalProof) otdr.ProofResult {
	set := otdr.NewProofSet()
	result := otdr.ProofResult{Passed: true}
	for _, directional := range proofs {
		added, err := set.Add(directional.Proof)
		if err != nil {
			continue
		}
		result = added
	}
	return result
}

func assess(chain *GenerationChain, thresholds Thresholds) (bool, []string) {
	var reasons []string
	scansPresent := len(chain.Scans) > 0
	undersized := false
	hasFinding := false
	if !scansPresent {
		reasons = append(reasons, "no scan recorded for this generation")
	}
	for _, scan := range chain.Scans {
		if scan.HasAnnotation(AnnotationInsufficient) {
			undersized = true
			reasons = append(reasons, fmt.Sprintf("scan %s has an undersized curve", scan.Trace.ID))
		}
		if scan.HasAnnotation(AnnotationBreak) || scan.HasAnnotation(AnnotationLossStep) {
			hasFinding = true
		}
	}
	if scansPresent && !hasFinding {
		reasons = append(reasons, "no break or abrupt loss step detected on any scan")
	}

	// Station channel: two landing stations locating the same fault within the
	// corroboration window. A single station can at best indicate a station
	// fault, never confirm a wet-span cut on its own.
	stationCorroboration := false
	if len(chain.Stations) == 1 {
		reasons = append(reasons, fmt.Sprintf("only station %s observed; peer station evidence missing", chain.Stations[0].StationID))
	}
	if len(chain.Stations) >= 2 {
		agrees := true
		for _, observation := range chain.Stations {
			if !observation.DistanceAgrees {
				agrees = false
				reasons = append(reasons, fmt.Sprintf("station fault distances disagree by %.2f km (limit %.2f km)", observation.DistanceSpreadKM, thresholds.BidirectionalSpreadKM))
				break
			}
		}
		stationCorroboration = hasFinding && agrees
	}

	// Proof channel: complete, passing bidirectional optical proof. This also
	// covers healthy acceptance scans, where the absence of a finding is
	// expected rather than a deficiency.
	proofsPass := chain.ProofResult.Complete && chain.ProofResult.Passed
	if !chain.ProofResult.Complete {
		seen := make(map[otdr.Direction]bool)
		for _, directional := range chain.Proofs {
			seen[directional.Direction] = true
		}
		for _, direction := range []otdr.Direction{otdr.DirectionEastToWest, otdr.DirectionWestToEast} {
			if !seen[direction] {
				reasons = append(reasons, "directional proof missing: "+string(direction))
			}
		}
	} else if !chain.ProofResult.Passed {
		reasons = append(reasons, "directional optical proof failed")
	}

	sufficient := scansPresent && !undersized && (stationCorroboration || proofsPass)
	return sufficient, reasons
}

func buildStations(byStation map[string][]ScanRecord, thresholds Thresholds) []StationObservation {
	stations := make([]StationObservation, 0, len(byStation))
	distances := make(map[string]float64)
	for stationID, records := range byStation {
		sort.Slice(records, func(i, j int) bool {
			if records[i].RecordedAt.Equal(records[j].RecordedAt) {
				return records[i].Trace.ID < records[j].Trace.ID
			}
			return records[i].RecordedAt.Before(records[j].RecordedAt)
		})
		observation := StationObservation{StationID: stationID}
		best := records[0]
		for _, record := range records {
			observation.ScanIDs = append(observation.ScanIDs, record.Trace.ID)
			if record.RecordedAt.After(observation.ObservedAt) {
				observation.ObservedAt = record.RecordedAt
			}
			if peakLoss(record) > peakLoss(best) {
				best = record
			}
		}
		observation.DistanceKM = faultDistance(best.Trace.Location)
		distances[stationID] = observation.DistanceKM
		stations = append(stations, observation)
	}
	sort.Slice(stations, func(i, j int) bool { return stations[i].StationID < stations[j].StationID })
	if len(stations) >= 2 {
		spread := distanceSpread(distances)
		agrees := spread <= thresholds.BidirectionalSpreadKM
		for index := range stations {
			stations[index].DistanceAgrees = agrees
			stations[index].DistanceSpreadKM = spread
		}
	}
	return stations
}

func peakLoss(record ScanRecord) float64 {
	peak := 0.0
	for _, chunk := range record.Trace.Chunks {
		if chunk.LossDB > peak {
			peak = chunk.LossDB
		}
	}
	return peak
}

func distanceSpread(distances map[string]float64) float64 {
	minimum, maximum := 0.0, 0.0
	first := true
	for _, value := range distances {
		if first || value < minimum {
			minimum = value
		}
		if first || value > maximum {
			maximum = value
		}
		first = false
	}
	return maximum - minimum
}

func orderedScans(records map[string]ScanRecord, scanIDs []string) []ScanRecord {
	scans := make([]ScanRecord, 0, len(scanIDs))
	for _, id := range scanIDs {
		scans = append(scans, records[id])
	}
	sort.Slice(scans, func(i, j int) bool {
		if scans[i].RecordedAt.Equal(scans[j].RecordedAt) {
			return scans[i].Trace.ID < scans[j].Trace.ID
		}
		return scans[i].RecordedAt.Before(scans[j].RecordedAt)
	})
	return scans
}

func chainHasScanInWindow(chain *GenerationChain, from, to time.Time) bool {
	for _, scan := range chain.Scans {
		if !from.IsZero() && scan.RecordedAt.Before(from) {
			continue
		}
		if !to.IsZero() && scan.RecordedAt.After(to) {
			continue
		}
		return true
	}
	return false
}

func recordedAt(trace otdr.Trace, now func() time.Time) time.Time {
	if !trace.CompletedAt.IsZero() {
		return trace.CompletedAt.UTC()
	}
	if !trace.StartedAt.IsZero() {
		return trace.StartedAt.UTC()
	}
	return now().UTC()
}
