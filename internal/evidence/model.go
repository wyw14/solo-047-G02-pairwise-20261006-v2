package evidence

import (
	"time"

	"github.com/wyw14/cry-103/internal/landing"
	"github.com/wyw14/cry-103/internal/otdr"
	"github.com/wyw14/cry-103/internal/span"
	"github.com/wyw14/cry-103/internal/topology"
)

// SegmentKind classifies a point on the segmented OTDR curve.
type SegmentKind string

const (
	SegmentAttenuation SegmentKind = "attenuation"
	SegmentLossStep    SegmentKind = "loss-step"
	SegmentBreak       SegmentKind = "break"
)

// Segment is one classified point of a scan's segmented curve.
type Segment struct {
	Index       int         `json:"index"`
	StationID   string      `json:"station_id"`
	DistanceKM  float64     `json:"distance_km"`
	LossDB      float64     `json:"loss_db"`
	DeltaLossDB float64     `json:"delta_loss_db"`
	Kind        SegmentKind `json:"kind"`
}

// AnnotationKind marks what an automated review found on a scan.
type AnnotationKind string

const (
	// AnnotationBreak marks the end-of-fibre reflection (suspected cut).
	AnnotationBreak AnnotationKind = "break"
	// AnnotationLossStep marks an abrupt, non-terminal loss increase.
	AnnotationLossStep AnnotationKind = "loss-step"
	// AnnotationInsufficient marks a scan that cannot support a conclusion.
	AnnotationInsufficient AnnotationKind = "insufficient-evidence"
)

// Severity follows the wording used by the integrity auditor.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityWarning  Severity = "warning"
	SeverityInfo     Severity = "info"
)

// Annotation is one automated finding attached to a scan record.
type Annotation struct {
	Kind       AnnotationKind `json:"kind"`
	Severity   Severity       `json:"severity"`
	DistanceKM float64        `json:"distance_km"`
	LossDB     float64        `json:"loss_db"`
	Message    string         `json:"message"`
}

// DirectionalProof records one direction's optical proof together with the
// generation it was measured against. Proofs from other generations can never
// complete the chain.
type DirectionalProof struct {
	Proof      otdr.Proof     `json:"proof"`
	Generation uint64         `json:"generation"`
	Direction  otdr.Direction `json:"direction"`
}

// StationObservation groups one landing station's view of a generation:
// its scans and, for two stations, how well the fault distances agree.
type StationObservation struct {
	StationID        string    `json:"station_id"`
	ScanIDs          []string  `json:"scan_ids"`
	DistanceKM       float64   `json:"distance_km"`
	DistanceAgrees   bool      `json:"distance_agrees"`
	DistanceSpreadKM float64   `json:"distance_spread_km"`
	ObservedAt       time.Time `json:"observed_at"`
}

// ScanRecord is one OTDR scan pinned to a single topology generation, with the
// segmented curve and automated annotations derived from its raw chunks. The
// trace is embedded so the original scan fields stay at the JSON top level.
type ScanRecord struct {
	otdr.Trace
	StationID   string       `json:"station_id"`
	Segments    []Segment    `json:"segments"`
	Annotations []Annotation `json:"annotations"`
	RecordedAt  time.Time    `json:"recorded_at"`
}

// HasAnnotation reports whether the record carries an annotation of the kind.
func (r ScanRecord) HasAnnotation(kind AnnotationKind) bool {
	for _, annotation := range r.Annotations {
		if annotation.Kind == kind {
			return true
		}
	}
	return false
}

// GenerationChain ties together every scan, directional proof and station
// observation that belongs to ONE (span, generation) pair. Mixing generations
// is structurally impossible: each chain has its own key.
type GenerationChain struct {
	SpanID              string               `json:"span_id"`
	Generation          uint64               `json:"generation"`
	Path                topology.Path        `json:"path"`
	ScanIDs             []string             `json:"scan_ids"`
	Scans               []ScanRecord         `json:"scans"`
	Stations            []StationObservation `json:"stations"`
	Proofs              []DirectionalProof   `json:"proofs"`
	ProofResult         otdr.ProofResult     `json:"proof_result"`
	Annotations         []Annotation         `json:"annotations"`
	EvidenceSufficient  bool                 `json:"evidence_sufficient"`
	InsufficientReasons []string             `json:"insufficient_reasons"`
	UpdatedAt           time.Time            `json:"updated_at"`
}

// IncidentEvidence is what incident handling and repair acceptance retrieve:
// the raw data for one line and time window, kept inside explicit generation
// chains so different topology generations are never merged.
type IncidentEvidence struct {
	SpanID      string            `json:"span_id"`
	IncidentID  string            `json:"incident_id,omitempty"`
	From        time.Time         `json:"from"`
	To          time.Time         `json:"to"`
	Generations []GenerationChain `json:"generations"`
	// CurrentGeneration, when non-zero, identifies the chain that reflects the
	// topology as it stands today. Older chains are returned for context only.
	CurrentGeneration uint64 `json:"current_generation,omitempty"`
}

// Thresholds controls the automated annotation of segmented curves.
type Thresholds struct {
	// BreakLossDB: a terminal reflection at or above this loss is a break.
	BreakLossDB float64
	// LossStepDB: a between-point loss jump at or above this value is a loss step.
	LossStepDB float64
	// BidirectionalSpreadKM: stations further apart than this on the fault
	// distance weaken the evidence instead of confirming it.
	BidirectionalSpreadKM float64
	// MinChunks: fewer curve points than this is insufficient evidence.
	MinChunks int
}

// DefaultThresholds matches the Locate heuristic (≈8 dB saturates confidence).
func DefaultThresholds() Thresholds {
	return Thresholds{BreakLossDB: 8, LossStepDB: 1.5, BidirectionalSpreadKM: 2, MinChunks: 2}
}

func chunksStation(chunks []landing.TraceChunk) string {
	for _, chunk := range chunks {
		if chunk.StationID != "" {
			return chunk.StationID
		}
	}
	return ""
}

func faultDistance(location span.FaultLocation) float64 {
	if len(location.Evidence) == 0 {
		return 0
	}
	return location.DistanceKM
}
