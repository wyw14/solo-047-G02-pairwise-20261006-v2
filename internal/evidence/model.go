package evidence

import (
	"time"

	"github.com/wyw14/cry-103/internal/otdr"
	"github.com/wyw14/cry-103/internal/topology"
)

type FindingKind string

const (
	FindingBreakpoint   FindingKind = "breakpoint"
	FindingLossStep     FindingKind = "loss-step"
	FindingInsufficient FindingKind = "insufficient-evidence"
)

type Finding struct {
	Kind       FindingKind `json:"kind"`
	Severity   string      `json:"severity"`
	DistanceKM float64     `json:"distance_km,omitempty"`
	Message    string      `json:"message"`
}

type Segment struct {
	Index        int     `json:"index"`
	FromKM       float64 `json:"from_km"`
	ToKM         float64 `json:"to_km"`
	StepLossDB   float64 `json:"step_loss_db"`
	SlopeDBPerKM float64 `json:"slope_db_per_km"`
}

type ProofLink struct {
	ProofID    string         `json:"proof_id"`
	Direction  otdr.Direction `json:"direction"`
	Passed     bool           `json:"passed"`
	LossDB     float64        `json:"loss_db"`
	MeasuredAt time.Time      `json:"measured_at"`
}

// Chain ties one OTDR scan to its topology generation, station, segment
// curve, and the directional proofs collected for the same generation.
type Chain struct {
	ID          string        `json:"id"`
	SpanID      string        `json:"span_id"`
	Generation  uint64        `json:"generation"`
	Path        topology.Path `json:"path"`
	TraceID     string        `json:"trace_id"`
	StationID   string        `json:"station_id"`
	StartedAt   time.Time     `json:"started_at"`
	CompletedAt time.Time     `json:"completed_at"`
	Confidence  float64       `json:"confidence"`
	Segments    []Segment     `json:"segments"`
	Proofs      []ProofLink   `json:"proofs"`
	Findings    []Finding     `json:"findings"`
	Sufficient  bool          `json:"sufficient"`
}
