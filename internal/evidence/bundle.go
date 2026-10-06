package evidence

import "time"

// GenerationSlice groups chains of one topology generation so a retrieval
// result can never blend scans from different generations.
type GenerationSlice struct {
	Generation uint64  `json:"generation"`
	Chains     []Chain `json:"chains"`
}

// Bundle is the raw-data retrieval result shared by incident handling and
// repair acceptance: every chain of one span inside a time range, sliced by
// topology generation.
type Bundle struct {
	SpanID      string            `json:"span_id"`
	From        time.Time         `json:"from"`
	To          time.Time         `json:"to"`
	Generations []GenerationSlice `json:"generations"`
}
