package evidence

import (
	"fmt"
	"math"
	"sort"

	"github.com/wyw14/cry-103/internal/landing"
	"github.com/wyw14/cry-103/internal/otdr"
)

// analyze turns one pinned scan's raw chunks into a segmented curve and the
// automated annotations over it. All chunks are already guaranteed to share
// the scan's generation (see otdr.assemble); analysis never crosses a boundary.
func analyze(trace otdr.Trace, thresholds Thresholds) ([]Segment, []Annotation) {
	chunks := sortChunks(trace.Chunks)
	segments := make([]Segment, 0, len(chunks))
	var annotations []Annotation

	breakIndex := -1
	for index, chunk := range chunks {
		delta := 0.0
		if index > 0 {
			delta = chunk.LossDB - chunks[index-1].LossDB
		}
		kind := SegmentAttenuation
		if delta >= thresholds.LossStepDB {
			kind = SegmentLossStep
			annotations = append(annotations, Annotation{
				Kind: AnnotationLossStep, Severity: SeverityWarning,
				DistanceKM: chunk.DistanceKM, LossDB: chunk.LossDB,
				Message: fmt.Sprintf("loss jumps %.2f dB at %.2f km", delta, chunk.DistanceKM),
			})
		}
		// A terminal high-loss reflection is the fibre end: the break point.
		if index == len(chunks)-1 && chunk.LossDB >= thresholds.BreakLossDB {
			kind = SegmentBreak
			breakIndex = index
		}
		segments = append(segments, Segment{
			Index: index, StationID: chunk.StationID, DistanceKM: chunk.DistanceKM,
			LossDB: chunk.LossDB, DeltaLossDB: delta, Kind: kind,
		})
	}
	if breakIndex >= 0 {
		chunk := chunks[breakIndex]
		annotations = append(annotations, Annotation{
			Kind: AnnotationBreak, Severity: SeverityCritical,
			DistanceKM: chunk.DistanceKM, LossDB: chunk.LossDB,
			Message: fmt.Sprintf("terminal reflection %.2f dB at %.2f km indicates a break", chunk.LossDB, chunk.DistanceKM),
		})
	}
	if len(chunks) < thresholds.MinChunks {
		annotations = append(annotations, Annotation{
			Kind: AnnotationInsufficient, Severity: SeverityWarning,
			Message: fmt.Sprintf("only %d curve point(s); at least %d are required", len(chunks), thresholds.MinChunks),
		})
	}
	return segments, annotations
}

func sortChunks(chunks []landing.TraceChunk) []landing.TraceChunk {
	ordered := append([]landing.TraceChunk(nil), chunks...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].StationID != ordered[j].StationID {
			return ordered[i].StationID < ordered[j].StationID
		}
		return ordered[i].Index < ordered[j].Index
	})
	return ordered
}

// stationDistanceAgrees reports whether two stations' fault localisations are
// close enough to corroborate each other, following span.Locate's ±2 km rule.
func stationDistanceAgrees(first, second float64, thresholds Thresholds) bool {
	return math.Abs(first-second) <= thresholds.BidirectionalSpreadKM
}
