package evidence

import (
	"fmt"
	"sort"

	"github.com/wyw14/cry-103/internal/landing"
	"github.com/wyw14/cry-103/internal/otdr"
)

type Thresholds struct {
	BreakpointStepDB float64
	LossStepDB       float64
	MinSegments      int
	MinConfidence    float64
}

func DefaultThresholds() Thresholds {
	return Thresholds{BreakpointStepDB: 5, LossStepDB: 1, MinSegments: 2, MinConfidence: 0.5}
}

func analyzeCurve(trace otdr.Trace, thresholds Thresholds) ([]Segment, []Finding) {
	chunks := append([]landing.TraceChunk(nil), trace.Chunks...)
	sort.Slice(chunks, func(i, j int) bool { return chunks[i].Index < chunks[j].Index })
	segments := make([]Segment, 0, len(chunks)-1)
	var findings []Finding
	for index := 1; index < len(chunks); index++ {
		from, to := chunks[index-1], chunks[index]
		step := to.LossDB - from.LossDB
		segment := Segment{Index: index - 1, FromKM: from.DistanceKM, ToKM: to.DistanceKM, StepLossDB: step}
		if span := to.DistanceKM - from.DistanceKM; span > 0 {
			segment.SlopeDBPerKM = step / span
		}
		segments = append(segments, segment)
		switch {
		case step >= thresholds.BreakpointStepDB:
			findings = append(findings, Finding{
				Kind: FindingBreakpoint, Severity: "critical", DistanceKM: to.DistanceKM,
				Message: fmt.Sprintf("loss jumps %.2f dB at %.2f km, fiber break suspected", step, to.DistanceKM),
			})
		case step >= thresholds.LossStepDB:
			findings = append(findings, Finding{
				Kind: FindingLossStep, Severity: "warning", DistanceKM: to.DistanceKM,
				Message: fmt.Sprintf("loss steps %.2f dB at %.2f km", step, to.DistanceKM),
			})
		}
	}
	return segments, findings
}

func sufficiency(confidence float64, segments []Segment, proofs []ProofLink, thresholds Thresholds) []Finding {
	var findings []Finding
	if len(segments) < thresholds.MinSegments {
		findings = append(findings, Finding{
			Kind: FindingInsufficient, Severity: "warning",
			Message: fmt.Sprintf("curve has %d segments, need at least %d", len(segments), thresholds.MinSegments),
		})
	}
	if confidence < thresholds.MinConfidence {
		findings = append(findings, Finding{
			Kind: FindingInsufficient, Severity: "warning",
			Message: fmt.Sprintf("fault-location confidence %.2f below %.2f", confidence, thresholds.MinConfidence),
		})
	}
	for _, direction := range []otdr.Direction{otdr.DirectionEastToWest, otdr.DirectionWestToEast} {
		proof := findProof(proofs, direction)
		switch {
		case proof == nil:
			findings = append(findings, Finding{
				Kind: FindingInsufficient, Severity: "warning",
				Message: fmt.Sprintf("directional proof %s is missing", direction),
			})
		case !proof.Passed:
			findings = append(findings, Finding{
				Kind: FindingInsufficient, Severity: "critical",
				Message: fmt.Sprintf("directional proof %s failed", direction),
			})
		}
	}
	return findings
}

func findProof(proofs []ProofLink, direction otdr.Direction) *ProofLink {
	for index := range proofs {
		if proofs[index].Direction == direction {
			return &proofs[index]
		}
	}
	return nil
}
