package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/wyw14/cry-103/internal/evidence"
	"github.com/wyw14/cry-103/internal/feed"
	"github.com/wyw14/cry-103/internal/incident"
	"github.com/wyw14/cry-103/internal/landing"
	"github.com/wyw14/cry-103/internal/otdr"
	"github.com/wyw14/cry-103/internal/platform"
	"github.com/wyw14/cry-103/internal/repair"
	"github.com/wyw14/cry-103/internal/repeater"
	"github.com/wyw14/cry-103/internal/route"
	"github.com/wyw14/cry-103/internal/span"
	"github.com/wyw14/cry-103/internal/store"
	"github.com/wyw14/cry-103/internal/switchover"
	"github.com/wyw14/cry-103/internal/telemetry"
	"github.com/wyw14/cry-103/internal/topology"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	clock := fixedClock{now: time.Date(2026, 8, 24, 3, 0, 0, 0, time.UTC)}
	ids := platform.UUIDGenerator{}
	registry := topology.NewRegistry()
	if _, err := registry.Register("S3", topology.PathPrimary, clock.Now()); err != nil {
		t.Fatal(err)
	}
	target, err := span.New("S3", "S3", 126, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	path, err := route.New("R-S3", "S3", clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	drainer := route.NewDrainer(0)
	activator := route.NewActivator(registry)
	receipts := landing.NewReceiptRegistry()
	permitService := repair.NewPermitService(feed.NewIsolationService(), receipts, ids, clock.Now)
	circuit, err := feed.NewCircuit("B", "east", clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	permitService.RegisterCircuit(circuit)
	repeaterAsset, err := repeater.New("R07", clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := incident.NewRepeaterLifecycle()
	projector := repeater.NewHealthProjector(lifecycle, 85)
	projector.Register(repeaterAsset)
	repository := telemetry.NewRepository()
	acceptance := repair.NewAcceptanceService(switchover.NewRestorer(drainer, activator, registry, ids, clock.Now), clock.Now)
	acceptance.Register("S3", target, path)
	services := &Services{
		Clock: clock, IDs: ids, Journal: store.NewJournal(filepath.Join(t.TempDir(), "events.jsonl")),
		Spans: map[string]*span.Span{"S3": target}, Routes: map[string]*route.Route{"R-S3": path},
		Repeaters: map[string]*repeater.Repeater{"R07": repeaterAsset}, Topology: registry,
		OTDR:        otdr.NewSessionService(registry, landing.NewScanner(registry, 0), ids, clock.Now),
		Evidence:    evidence.NewLedger(evidence.DefaultThresholds(), clock.Now),
		Switchovers: switchover.NewSessionService(drainer, activator, ids, clock.Now, time.Second),
		Permits:     permitService, Receipts: receipts, Acceptance: acceptance,
		Telemetry: telemetry.NewIngestor(repository, clock.Now, projector), TelemetryRepository: repository,
		Admission:  incident.NewAdmissionService(telemetry.NewMuteManager(clock.Now)),
		Classifier: incident.NewClassifier(), Correlator: otdr.NewCorrelator(),
		Incidents: make(map[string]*incident.Incident),
	}
	return NewServer(services)
}

type fixedClock struct {
	now time.Time
}

func (c fixedClock) Now() time.Time {
	return c.now
}

func doRequest(server *Server, method, target string, body any) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(method, target, reader))
	return recorder
}

func TestEvidenceChainPinsGenerationAcrossIncidentAndRepair(t *testing.T) {
	server := testServer(t)

	// Generation 1: a long enough scan synthesises a terminal >=8 dB break.
	scan1 := doRequest(server, http.MethodPost, "/api/spans/S3/otdr-scans", ScanRequest{StationID: "east", LengthKM: 126, Chunks: 12})
	if scan1.Code != http.StatusCreated {
		t.Fatalf("scan1 status=%d body=%s", scan1.Code, scan1.Body.String())
	}
	var record1 evidence.ScanRecord
	if err := json.Unmarshal(scan1.Body.Bytes(), &record1); err != nil {
		t.Fatal(err)
	}
	if record1.Generation != 1 || !record1.HasAnnotation(evidence.AnnotationBreak) {
		t.Fatalf("expected a generation-1 scan with a break, got gen=%d annotations=%+v", record1.Generation, record1.Annotations)
	}

	// The incident is raised while generation 1 is in service.
	local := doRequest(server, http.MethodPost, "/api/spans/S3/incidents/INC-1", IncidentRequest{Action: "local-complete"})
	if local.Code != http.StatusOK {
		t.Fatalf("local-complete status=%d body=%s", local.Code, local.Body.String())
	}

	// Topology switches to protection, bumping the generation to 2.
	if _, err := server.services.Topology.Switch("S3", topology.PathProtection, server.services.Clock.Now()); err != nil {
		t.Fatal(err)
	}
	scan2 := doRequest(server, http.MethodPost, "/api/spans/S3/otdr-scans", ScanRequest{StationID: "east", LengthKM: 126, Chunks: 3})
	if scan2.Code != http.StatusCreated {
		t.Fatalf("scan2 status=%d body=%s", scan2.Code, scan2.Body.String())
	}
	var record2 evidence.ScanRecord
	if err := json.Unmarshal(scan2.Body.Bytes(), &record2); err != nil {
		t.Fatal(err)
	}
	if record2.Generation != 2 || record2.Path != topology.PathProtection {
		t.Fatalf("expected a generation-2 protection scan, got gen=%d path=%s", record2.Generation, record2.Path)
	}

	// A post-switch (gen 2) scan must be rejected as the gen-1 incident's peer.
	rejected := doRequest(server, http.MethodPost, "/api/spans/S3/incidents/INC-1", IncidentRequest{
		Action: "peer-evidence", StationID: "west", TraceID: record2.ID, DistanceKM: 30,
	})
	if rejected.Code != http.StatusConflict {
		t.Fatalf("cross-generation peer evidence must be rejected, status=%d body=%s", rejected.Code, rejected.Body.String())
	}
	// A same-generation (gen 1) scan is accepted.
	accepted := doRequest(server, http.MethodPost, "/api/spans/S3/incidents/INC-1", IncidentRequest{
		Action: "peer-evidence", StationID: "west", TraceID: record1.ID, DistanceKM: 56,
	})
	if accepted.Code != http.StatusOK {
		t.Fatalf("same-generation peer evidence must be accepted, status=%d body=%s", accepted.Code, accepted.Body.String())
	}

	// Incident retrieval returns only the bound generation 1 chain.
	incidentBundle := doRequest(server, http.MethodGet, "/api/spans/S3/incidents/INC-1/evidence", nil)
	if incidentBundle.Code != http.StatusOK {
		t.Fatalf("incident evidence status=%d body=%s", incidentBundle.Code, incidentBundle.Body.String())
	}
	var bundle evidence.IncidentEvidence
	if err := json.Unmarshal(incidentBundle.Body.Bytes(), &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.CurrentGeneration != 1 || len(bundle.Generations) != 1 {
		t.Fatalf("incident evidence must stay inside generation 1, got %+v", bundle.Generations)
	}

	// Span-level retrieval lists two distinct, ordered generation chains.
	spanBundle := doRequest(server, http.MethodGet, "/api/spans/S3/evidence", nil)
	var all struct {
		Generations []evidence.GenerationChain `json:"generations"`
	}
	if err := json.Unmarshal(spanBundle.Body.Bytes(), &all); err != nil {
		t.Fatal(err)
	}
	if len(all.Generations) != 2 || all.Generations[0].Generation != 1 || all.Generations[1].Generation != 2 {
		t.Fatalf("expected two separate generation chains, got %+v", all.Generations)
	}

	// An acceptance proof now anchors to the in-service generation 2 and must
	// never complete the generation 1 chain.
	proof := doRequest(server, http.MethodPost, "/api/repairs/S3/acceptance", AcceptanceRequest{
		AcceptanceID: "ACC-1", Direction: otdr.DirectionEastToWest, Passed: true, LossDB: 0.4,
	})
	if proof.Code != http.StatusAccepted {
		t.Fatalf("acceptance proof status=%d body=%s", proof.Code, proof.Body.String())
	}
	repairBundle := doRequest(server, http.MethodGet, "/api/repairs/S3/evidence", nil)
	var repair struct {
		CurrentGeneration uint64                     `json:"current_generation"`
		Generations       []evidence.GenerationChain `json:"generations"`
	}
	if err := json.Unmarshal(repairBundle.Body.Bytes(), &repair); err != nil {
		t.Fatal(err)
	}
	if repair.CurrentGeneration != 2 || len(repair.Generations) != 1 || len(repair.Generations[0].Proofs) != 1 {
		t.Fatalf("acceptance proof must anchor to generation 2, got %+v", repair)
	}
	gen1Only := doRequest(server, http.MethodGet, "/api/spans/S3/evidence?generation=1", nil)
	var gen1 struct {
		Generations []evidence.GenerationChain `json:"generations"`
	}
	if err := json.Unmarshal(gen1Only.Body.Bytes(), &gen1); err != nil {
		t.Fatal(err)
	}
	if len(gen1.Generations) != 1 || len(gen1.Generations[0].Proofs) != 0 {
		t.Fatalf("the generation-1 chain must not carry the generation-2 proof, got %+v", gen1.Generations)
	}
}

func TestHealthAndScanHTTPFlow(t *testing.T) {
	server := testServer(t)
	health := httptest.NewRecorder()
	server.Handler().ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status=%d body=%s", health.Code, health.Body.String())
	}

	body, _ := json.Marshal(ScanRequest{StationID: "east", LengthKM: 126, Chunks: 3})
	scan := httptest.NewRecorder()
	server.Handler().ServeHTTP(scan, httptest.NewRequest(http.MethodPost, "/api/spans/S3/otdr-scans", bytes.NewReader(body)))
	if scan.Code != http.StatusCreated {
		t.Fatalf("scan status=%d body=%s", scan.Code, scan.Body.String())
	}
	var trace otdr.Trace
	if err := json.Unmarshal(scan.Body.Bytes(), &trace); err != nil {
		t.Fatal(err)
	}
	if trace.SpanID != "S3" || len(trace.Chunks) != 3 {
		t.Fatalf("unexpected trace: %+v", trace)
	}

	status := httptest.NewRecorder()
	server.Handler().ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/api/system/status", nil))
	if status.Code != http.StatusOK {
		t.Fatalf("status code=%d body=%s", status.Code, status.Body.String())
	}
}
