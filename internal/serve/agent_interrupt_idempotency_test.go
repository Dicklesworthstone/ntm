package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// POST /api/v1/sessions/{id}/agents/interrupt carries the Idempotency-Key
// header into the durable operation ID and surfaces the idempotency refusals
// as client conflicts (409), not server faults.
func TestHandleAgentInterruptV1IdempotencyRefusalsAreConflicts(t *testing.T) {
	srv, _ := setupTestServer(t)

	for _, code := range []string{robot.ErrCodeIdempotencyConflict, robot.ErrCodeOperationInProgress, robot.ErrCodeOperationOutcomeUnknown} {
		t.Run(code, func(t *testing.T) {
			var gotKey string
			srv.interruptAgents = func(opts robot.InterruptOptions) (*robot.InterruptOutput, error) {
				gotKey = opts.IdempotencyKey
				return &robot.InterruptOutput{RobotResponse: robot.NewErrorResponse(
					fmt.Errorf("operation 'retask-7' refused"), code, "Query the durable receipt with --robot-send-receipt=retask-7",
				)}, nil
			}

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/proj/agents/interrupt",
				strings.NewReader(`{"message":"change course","force":true}`))
			req.Header.Set("Idempotency-Key", "retask-7")
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("sessionId", "proj")
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

			srv.handleAgentInterruptV1(rec, req)

			if gotKey != "retask-7" {
				t.Fatalf("IdempotencyKey = %q, want the Idempotency-Key header", gotKey)
			}
			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body: %s", rec.Code, rec.Body.String())
			}
			var response APIError
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			if response.Success || response.ErrorCode != code {
				t.Fatalf("response = %+v, want error_code %s", response, code)
			}
		})
	}
}

// Both HTTP actuation adapters must retain the durable receipt on errors.
// Without it, a caller recovering from a timeout loses the original targets
// and cannot distinguish an unstarted operation from ambiguous delivery.
func TestHandleAgentActuationV1PreservesUnknownOperationReceipt(t *testing.T) {
	for _, kind := range []string{"send", "interrupt"} {
		t.Run(kind, func(t *testing.T) {
			srv, _ := setupTestServer(t)
			started := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
			op := &robot.OperationInfo{
				OperationID: "recover-7", Kind: kind, Status: "in_progress",
				PayloadSHA256: strings.Repeat("a", 64), PayloadBytes: 19,
				CreatedAt: started.Add(-time.Minute), DispatchStartedAt: &started,
				Admissions: []robot.OperationAdmission{{Target: "0.8", State: robot.AdmissionUnknown}},
			}
			response := robot.NewErrorResponse(fmt.Errorf("prior delivery is unresolved"),
				robot.ErrCodeOperationOutcomeUnknown, "Inspect --robot-send-receipt=recover-7 before deciding to send again")
			srv.sendAgents = func(opts robot.SendOptions) (*robot.SendOutput, error) {
				if opts.IdempotencyKey != op.OperationID {
					t.Fatalf("operation ID = %q", opts.IdempotencyKey)
				}
				return &robot.SendOutput{RobotResponse: response, Operation: op, Warnings: []string{"prior outcome not recorded"}}, nil
			}
			srv.interruptAgents = func(opts robot.InterruptOptions) (*robot.InterruptOutput, error) {
				if opts.IdempotencyKey != op.OperationID {
					t.Fatalf("operation ID = %q", opts.IdempotencyKey)
				}
				return &robot.InterruptOutput{RobotResponse: response, Operation: op, Warnings: []string{"prior outcome not recorded"}}, nil
			}

			req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/proj/agents/"+kind,
				strings.NewReader(`{"message":"resume requested work"}`))
			req.Header.Set("Idempotency-Key", op.OperationID)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("sessionId", "proj")
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
			rec := httptest.NewRecorder()
			if kind == "send" {
				srv.handleAgentSendV1(rec, req)
			} else {
				srv.handleAgentInterruptV1(rec, req)
			}
			var body struct {
				ErrorCode string `json:"error_code"`
				Hint      string `json:"hint"`
				Details   struct {
					Operation *robot.OperationInfo `json:"operation"`
					Warnings  []string             `json:"warnings"`
				} `json:"details"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if rec.Code != http.StatusConflict || body.ErrorCode != robot.ErrCodeOperationOutcomeUnknown {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			got := body.Details.Operation
			if got == nil || got.OperationID != op.OperationID || got.Kind != kind || got.Status != "in_progress" ||
				got.PayloadSHA256 != op.PayloadSHA256 || got.PayloadBytes != op.PayloadBytes ||
				got.DispatchStartedAt == nil || !got.DispatchStartedAt.Equal(started) ||
				len(got.Admissions) != 1 || got.Admissions[0] != op.Admissions[0] {
				t.Fatalf("HTTP error lost the original operation receipt: %s", rec.Body.String())
			}
			if body.Hint != response.Hint || len(body.Details.Warnings) != 1 {
				t.Fatalf("HTTP error lost recovery diagnostics: %s", rec.Body.String())
			}
		})
	}
}

// TestHandleAgentInterruptV1IdempotencyKeyIsDurable drives the real handler,
// middleware and robot.GetInterrupt against a real tmux fixture pane:
//
//   - the first POST interrupts once and delivers the task once;
//   - an identical POST to the same process is answered by the HTTP replay
//     cache;
//   - after a simulated server restart (the in-memory replay cache is gone)
//     the identical POST is answered by the durable operation record —
//     replayed, with no second Ctrl+C and no second delivery;
//   - reusing the key with a different body is a 409 IDEMPOTENCY_CONFLICT.
func TestHandleAgentInterruptV1IdempotencyKeyIsDurable(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	srv, store := setupTestServer(t)

	robot.SetProjectionStore(store)
	t.Cleanup(func() { robot.SetProjectionStore(nil) })
	feed := robot.NewAttentionFeed(robot.AttentionFeedConfig{JournalSize: 100, RetentionPeriod: time.Hour})
	oldFeed := robot.GetAttentionFeed()
	robot.SetAttentionFeed(feed)
	t.Cleanup(func() {
		robot.SetAttentionFeed(oldFeed)
		feed.Stop()
	})

	fx := testutil.StartInterruptFixture(t, "rest")
	marker := fmt.Sprintf("ntm-rest-idem-%d", time.Now().UnixNano())
	key := "retask-" + marker

	post := func(task string) (*httptest.ResponseRecorder, map[string]any) {
		t.Helper()
		body := fmt.Sprintf(`{"panes":[%q],"message":%q,"force":true,"no_wait":true}`, fx.PaneID, task)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+fx.Session+"/agents/interrupt", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		var payload map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode response (status %d): %v\nbody=%s", rec.Code, err, rec.Body.String())
		}
		return rec, payload
	}
	operationOf := func(payload map[string]any) map[string]any {
		op, _ := payload["operation"].(map[string]any)
		return op
	}

	firstRec, first := post("new task " + marker)
	if firstRec.Code != http.StatusOK || first["success"] != true {
		t.Fatalf("first POST status=%d payload=%v, want success", firstRec.Code, first)
	}
	if op := operationOf(first); op == nil || op["kind"] != "interrupt" || op["operation_id"] != key || op["replayed"] == true {
		t.Fatalf("first operation = %v, want a fresh interrupt operation bound to the Idempotency-Key", op)
	}
	fx.WaitForEvents(t, marker, 1, 1)

	cachedRec, _ := post("new task " + marker)
	fx.AssertQuiet(t, marker, 1, 1)
	if cachedRec.Code != http.StatusOK || cachedRec.Header().Get("X-Idempotent-Replay") != "true" {
		t.Fatalf("same-process retry status=%d replay-header=%q, want the HTTP cache replay",
			cachedRec.Code, cachedRec.Header().Get("X-Idempotent-Replay"))
	}

	// Simulated restart: the HTTP replay cache is per-process memory; the
	// durable operation record in the state store is what survives.
	previousCache := srv.idempotencyStore
	srv.idempotencyStore = NewIdempotencyStore(24 * time.Hour)
	previousCache.Stop()

	durableRec, durable := post("new task " + marker)
	fx.AssertQuiet(t, marker, 1, 1)
	if durableRec.Code != http.StatusOK || durable["success"] != true {
		t.Fatalf("post-restart retry status=%d payload=%v, want the replayed success", durableRec.Code, durable)
	}
	if durableRec.Header().Get("X-Idempotent-Replay") == "true" {
		t.Fatal("post-restart retry was served by the HTTP cache; the durable operation record was not exercised")
	}
	if op := operationOf(durable); op == nil || op["replayed"] != true || op["kind"] != "interrupt" {
		t.Fatalf("post-restart operation = %v, want the durable interrupt replay", op)
	}
	if durable["interrupted_at"] != first["interrupted_at"] {
		t.Fatalf("post-restart interrupted_at = %v, want the recorded %v", durable["interrupted_at"], first["interrupted_at"])
	}

	// A different body under the same key: the same process's HTTP cache
	// rejects it (422), and after another restart the durable binding does
	// (409 IDEMPOTENCY_CONFLICT). Neither touches the pane.
	cachedConflictRec, cachedConflict := post("different task " + marker)
	fx.AssertQuiet(t, marker, 1, 1)
	if cachedConflictRec.Code != http.StatusUnprocessableEntity || cachedConflict["error_code"] != ErrCodeIdempotentReplay {
		t.Fatalf("same-process conflicting reuse status=%d payload=%v, want 422 %s",
			cachedConflictRec.Code, cachedConflict, ErrCodeIdempotentReplay)
	}

	previousCache = srv.idempotencyStore
	srv.idempotencyStore = NewIdempotencyStore(24 * time.Hour)
	previousCache.Stop()

	conflictRec, conflict := post("different task " + marker)
	fx.AssertQuiet(t, marker, 1, 1)
	if conflictRec.Code != http.StatusConflict || conflict["error_code"] != robot.ErrCodeIdempotencyConflict {
		t.Fatalf("post-restart conflicting reuse status=%d payload=%v, want 409 IDEMPOTENCY_CONFLICT", conflictRec.Code, conflict)
	}
}

func TestRequestIdentityLifecycleEndpointsV1(t *testing.T) {
	srv, _ := setupTestServer(t)
	router := srv.Router()

	doReq := func(method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
		t.Helper()
		var reader *strings.Reader
		if body != "" {
			reader = strings.NewReader(body)
		} else {
			reader = strings.NewReader("")
		}
		req := httptest.NewRequest(method, path, reader)
		req.Header.Set("X-Request-Id", "http-corr-id-999")
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		var payload map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("%s %s invalid JSON (%d): %v\n%s", method, path, rec.Code, err, rec.Body.String())
		}
		return rec, payload
	}

	// 1. Unknown request ID -> 404 NOT_FOUND.
	rec404, body404 := doReq(http.MethodGet, "/api/v1/requests/missing-op", "")
	if rec404.Code != http.StatusNotFound || body404["error_code"] != robot.ErrCodeNotFound {
		t.Fatalf("GET missing status=%d body=%v, want 404 NOT_FOUND", rec404.Code, body404)
	}

	// 2. Claim pre-dispatch send operation -> GET preserves target request_id (not HTTP X-Request-Id) -> POST /cancel succeeds -> repeat /cancel returns 409 REQUEST_NOT_CANCELLABLE.
	op1 := &state.SendOperation{
		OperationID:   "op-rest-cancel-1",
		SessionName:   "proj",
		Kind:          state.OperationKindSend,
		ClaimToken:    "tok-1",
		BindingHash:   strings.Repeat("1", 64),
		PayloadSHA256: strings.Repeat("2", 64),
		PayloadBytes:  5,
		Targets:       []string{"1"},
	}
	_, claimed1, err := srv.stateStore.ClaimSendOperation(op1)
	if err != nil || !claimed1 {
		t.Fatalf("ClaimSendOperation op-rest-cancel-1: claimed=%v err=%v", claimed1, err)
	}

	stRec, stBody := doReq(http.MethodGet, "/api/v1/requests/op-rest-cancel-1", "")
	if stRec.Code != http.StatusOK || stBody["success"] != true || stBody["request_id"] != "op-rest-cancel-1" || stBody["cancellable"] != true {
		t.Fatalf("GET /api/v1/requests/op-rest-cancel-1 status=%d body=%v", stRec.Code, stBody)
	}

	cancelRec, cancelBody := doReq(http.MethodPost, "/api/v1/requests/op-rest-cancel-1/cancel", "")
	if cancelRec.Code != http.StatusOK || cancelBody["canceled"] != true || cancelBody["request_id"] != "op-rest-cancel-1" || cancelBody["status"] != state.SendOperationCanceled {
		t.Fatalf("POST /cancel status=%d body=%v", cancelRec.Code, cancelBody)
	}

	cancelAgainRec, cancelAgainBody := doReq(http.MethodPost, "/api/v1/requests/op-rest-cancel-1/cancel", "")
	if cancelAgainRec.Code != http.StatusConflict || cancelAgainBody["error_code"] != "REQUEST_NOT_CANCELLABLE" {
		t.Fatalf("second POST /cancel status=%d body=%v, want 409 REQUEST_NOT_CANCELLABLE", cancelAgainRec.Code, cancelAgainBody)
	}

	// 3. Dispatched operation -> POST /cancel returns 409 REQUEST_ALREADY_DISPATCHED; POST /wait times out (504) then returns 200 once completed.
	op2 := &state.SendOperation{
		OperationID:   "op-rest-wait-2",
		SessionName:   "proj",
		Kind:          state.OperationKindSend,
		BindingHash:   strings.Repeat("3", 64),
		PayloadSHA256: strings.Repeat("4", 64),
		PayloadBytes:  5,
		Targets:       []string{"1"},
	}
	claimedOp2, claimed2, err := srv.stateStore.ClaimSendOperation(op2)
	if err != nil || !claimed2 || claimedOp2 == nil {
		t.Fatalf("ClaimSendOperation op-rest-wait-2: claimed=%v op=%v err=%v", claimed2, claimedOp2, err)
	}
	if _, err := srv.stateStore.StartSendOperation(claimedOp2); err != nil {
		t.Fatalf("StartSendOperation: %v", err)
	}

	dispatchedCancelRec, dispatchedCancelBody := doReq(http.MethodPost, "/api/v1/requests/op-rest-wait-2/cancel", "")
	if dispatchedCancelRec.Code != http.StatusConflict || dispatchedCancelBody["error_code"] != "REQUEST_ALREADY_DISPATCHED" {
		t.Fatalf("POST /cancel dispatched status=%d body=%v, want 409 REQUEST_ALREADY_DISPATCHED", dispatchedCancelRec.Code, dispatchedCancelBody)
	}

	waitTimeoutRec, waitTimeoutBody := doReq(http.MethodPost, "/api/v1/requests/op-rest-wait-2/wait", `{"timeout_ms":30,"poll_ms":10}`)
	if waitTimeoutRec.Code != http.StatusGatewayTimeout || waitTimeoutBody["error_code"] != robot.ErrCodeTimeout {
		t.Fatalf("POST /wait timeout status=%d body=%v, want 504 TIMEOUT", waitTimeoutRec.Code, waitTimeoutBody)
	}

	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = srv.stateStore.CompleteSendOperation("op-rest-wait-2", "proj", claimedOp2.ClaimToken, `{"success":true,"targets":["1"],"successful":["1"]}`, time.Now().UTC())
	}()
	waitDoneRec, waitDoneBody := doReq(http.MethodPost, "/api/v1/requests/op-rest-wait-2/wait", `{"timeout":"2s","poll":"10ms"}`)
	if waitDoneRec.Code != http.StatusOK || waitDoneBody["terminal"] != true || waitDoneBody["status"] != state.SendOperationCompleted || waitDoneBody["request_id"] != "op-rest-wait-2" {
		t.Fatalf("POST /wait settled status=%d body=%v", waitDoneRec.Code, waitDoneBody)
	}
}
