package handoff

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

type sourceLeaseMutation struct {
	kind, owner string
	paths       []string
	ids         []int
}

// This independent registry never derives live ownership from the request or
// repairs the captured handoff. Union selectors deliberately reproduce how a
// path fallback can mutate a replacement lease even when an ID is also given.
type sourceLeaseRegistry struct {
	live           map[int]agentmail.FileReservation
	listing        []agentmail.FileReservation
	listErr        error
	reads          int
	afterRead      func()
	beforeMutation func()
	mutations      []sourceLeaseMutation
	ackCount       *int
	mutationErr    error
	mutateIDs      bool
	acquisitions   int
}

func sourceLeaseFixture() (*sourceLeaseRegistry, TransferReservationsOptions) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.UTC)
	expires := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	client := &sourceLeaseRegistry{live: map[int]agentmail.FileReservation{
		11: {ID: 11, ProjectID: 73, AgentName: "old", PathPattern: "a.go", Exclusive: true, Reason: "edit",
			CreatedTS: agentmail.FlexTime{Time: created}, ExpiresTS: agentmail.FlexTime{Time: expires}},
		22: {ID: 22, ProjectID: 73, AgentName: "old", PathPattern: "b.go", Exclusive: false, Reason: "read",
			CreatedTS: agentmail.FlexTime{Time: created}, ExpiresTS: agentmail.FlexTime{Time: expires}},
		33: {ID: 33, ProjectID: 73, AgentName: "old", PathPattern: "independent.go", Exclusive: true, Reason: "other work",
			CreatedTS: agentmail.FlexTime{Time: created}, ExpiresTS: agentmail.FlexTime{Time: expires}},
	}}
	// Deliberately different order from the registry and from canonical paths.
	opts := TransferReservationsOptions{
		ProjectKey: "project", FromAgent: "old", ToAgent: "new", GracePeriod: time.Nanosecond,
		Reservations: []ReservationSnapshot{
			{ID: 22, ProjectID: 73, AgentName: "old", PathPattern: "b.go", Exclusive: false, Reason: "read", CreatedAt: created, ExpiresAt: expires},
			{ID: 11, ProjectID: 73, AgentName: "old", PathPattern: "a.go", Exclusive: true, Reason: "edit", CreatedAt: created, ExpiresAt: expires},
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return client, opts
}

func (c *sourceLeaseRegistry) ListReservations(ctx context.Context, project, owner string, all bool) ([]agentmail.FileReservation, error) {
	c.reads++
	if project != "project" || owner != "" || !all {
		return nil, errors.New("source verification did not request the full project listing")
	}
	rows := append([]agentmail.FileReservation(nil), c.listing...)
	if c.listing == nil {
		for _, row := range c.live {
			rows = append(rows, row)
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	}
	if c.afterRead != nil {
		c.afterRead()
	}
	return rows, errors.Join(c.listErr, ctx.Err())
}

func (c *sourceLeaseRegistry) selectLeases(kind, owner string, paths []string, ids []int) (int, error) {
	if c.beforeMutation != nil {
		c.beforeMutation()
	}
	c.mutations = append(c.mutations, sourceLeaseMutation{kind, owner, append([]string(nil), paths...), append([]int(nil), ids...)})
	count := 0
	for id, row := range c.live {
		selected := len(paths) == 0 && len(ids) == 0 // Unscoped selection is broad, not a harmless no-op.
		for _, path := range paths {
			selected = selected || row.PathPattern == path
		}
		for _, wanted := range ids {
			selected = selected || id == wanted
		}
		if !selected || row.AgentName != owner {
			continue
		}
		count++
		if kind == "release" {
			delete(c.live, id)
		} else {
			row.ExpiresTS.Time = row.ExpiresTS.Add(time.Hour)
			c.live[id] = row
		}
	}
	if c.mutateIDs && len(ids) > 0 {
		ids[0] = -1
	}
	if c.ackCount != nil {
		count = *c.ackCount
	}
	return count, c.mutationErr
}

func (c *sourceLeaseRegistry) ReleaseReservations(_ context.Context, _, owner string, paths []string, ids []int) (*agentmail.ReleaseReservationsResult, error) {
	count, err := c.selectLeases("release", owner, paths, ids)
	return &agentmail.ReleaseReservationsResult{Released: count}, err
}
func (c *sourceLeaseRegistry) RenewReservations(_ context.Context, o agentmail.RenewReservationsOptions) (*agentmail.RenewReservationsResult, error) {
	count, err := c.selectLeases("renew", o.AgentName, o.Paths, o.ReservationIDs)
	return &agentmail.RenewReservationsResult{Renewed: count}, err
}
func (c *sourceLeaseRegistry) ReservePaths(_ context.Context, o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
	c.acquisitions++
	result := &agentmail.ReservationResult{}
	for i, path := range o.Paths {
		result.Granted = append(result.Granted, agentmail.FileReservation{ID: 900 + c.acquisitions*10 + i, PathPattern: path})
	}
	return result, nil
}

func TestTransferSourceLeaseSelectorsAndEvidence(t *testing.T) {
	for _, sameAgent := range []bool{false, true} {
		t.Run(map[bool]string{false: "release", true: "renew"}[sameAgent], func(t *testing.T) {
			client, opts := sourceLeaseFixture()
			if sameAgent {
				opts.ToAgent = opts.FromAgent
			}
			before, _ := json.Marshal(opts.Reservations)
			independent := client.live[33]
			client.mutateIDs = true
			result, err := TransferReservations(context.Background(), client, opts)
			if err != nil || !result.Success || result.OutcomeUnknown || result.RolledBack {
				t.Fatalf("valid source leases rejected: %+v %v", result, err)
			}
			if client.reads != 1 || len(client.mutations) != 1 || len(client.mutations[0].paths) != 0 ||
				!reflect.DeepEqual(client.mutations[0].ids, []int{11, 22}) || client.mutations[0].owner != "old" {
				t.Fatalf("source selection is not exact-ID scoped: %+v reads=%d", client.mutations, client.reads)
			}
			if !reflect.DeepEqual(client.live[33], independent) {
				t.Fatal("source mutation changed independent work")
			}
			after, _ := json.Marshal(opts.Reservations)
			if string(before) != string(after) {
				t.Fatal("transfer sorted or modified caller-owned snapshot")
			}
			wire, _ := json.Marshal(result)
			var evidence struct {
				Requested []int `json:"requested_ids"`
				Released  []int `json:"released_ids"`
				Granted   []int `json:"granted_ids"`
			}
			if err := json.Unmarshal(wire, &evidence); err != nil || !reflect.DeepEqual(evidence.Requested, []int{11, 22}) {
				t.Fatalf("source IDs lost from result: %s %v", wire, err)
			}
			if sameAgent {
				if client.acquisitions != 0 || len(evidence.Released) != 0 || !reflect.DeepEqual(evidence.Granted, []int{11, 22}) {
					t.Fatalf("renewal reported new or released leases: %s", wire)
				}
			} else if !reflect.DeepEqual(evidence.Released, []int{11, 22}) || client.acquisitions != 2 {
				t.Fatalf("source-release evidence or mode groups lost: %s acquisitions=%d", wire, client.acquisitions)
			}
		})
	}
}

func TestTransferSourceRejectsInvalidCaptureWithoutIO(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*TransferReservationsOptions)
	}{
		{"missing ID", func(o *TransferReservationsOptions) { o.Reservations[0].ID = 0 }},
		{"negative ID", func(o *TransferReservationsOptions) { o.Reservations[0].ID = -1 }},
		{"missing project", func(o *TransferReservationsOptions) { o.Reservations[0].ProjectID = 0 }},
		{"missing owner", func(o *TransferReservationsOptions) { o.Reservations[0].AgentName = "" }},
		{"foreign owner", func(o *TransferReservationsOptions) { o.Reservations[0].AgentName = "peer" }},
		{"missing creation", func(o *TransferReservationsOptions) { o.Reservations[0].CreatedAt = time.Time{} }},
		{"missing expiry", func(o *TransferReservationsOptions) { o.Reservations[0].ExpiresAt = time.Time{} }},
		{"impossible lifetime", func(o *TransferReservationsOptions) { o.Reservations[0].ExpiresAt = o.Reservations[0].CreatedAt }},
		{"blank path", func(o *TransferReservationsOptions) { o.Reservations[0].PathPattern = "  " }},
		{"only empty path", func(o *TransferReservationsOptions) { o.Reservations = []ReservationSnapshot{{}} }},
		{"duplicate IDs", func(o *TransferReservationsOptions) { o.Reservations[0].ID = o.Reservations[1].ID }},
		{"duplicate paths with different IDs and modes", func(o *TransferReservationsOptions) { o.Reservations[0].PathPattern = o.Reservations[1].PathPattern }},
		{"identical duplicate", func(o *TransferReservationsOptions) { o.Reservations = append(o.Reservations, o.Reservations[0]) }},
		{"mixed projects", func(o *TransferReservationsOptions) { o.Reservations[0].ProjectID = 74 }},
		{"legacy path-only snapshot", func(o *TransferReservationsOptions) {
			o.Reservations = []ReservationSnapshot{{PathPattern: "a.go", Exclusive: true}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, opts := sourceLeaseFixture()
			tc.change(&opts)
			before, _ := json.Marshal(opts.Reservations)
			result, err := TransferReservations(context.Background(), client, opts)
			if !errors.Is(err, ErrTransferSourceEvidence) || result.Success || result.OutcomeUnknown || result.Stage != "validate" {
				t.Fatalf("invalid capture was accepted or mislabeled: %+v %v", result, err)
			}
			if client.reads != 0 || len(client.mutations) != 0 || client.acquisitions != 0 {
				t.Fatalf("invalid capture reached external I/O: %+v", client)
			}
			after, _ := json.Marshal(opts.Reservations)
			if string(before) != string(after) {
				t.Fatal("preflight rewrote invalid capture evidence")
			}
		})
	}
}

func TestTransferSourceRejectsStaleReadbackBeforeAnyMutation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(agentmail.FileReservation) agentmail.FileReservation
	}{
		{"replacement ID", func(r agentmail.FileReservation) agentmail.FileReservation { r.ID = 122; return r }},
		{"foreign project", func(r agentmail.FileReservation) agentmail.FileReservation { r.ProjectID = 74; return r }},
		{"foreign owner", func(r agentmail.FileReservation) agentmail.FileReservation { r.AgentName = "peer"; return r }},
		{"changed path", func(r agentmail.FileReservation) agentmail.FileReservation { r.PathPattern = "other.go"; return r }},
		{"shared became exclusive", func(r agentmail.FileReservation) agentmail.FileReservation { r.Exclusive = true; return r }},
		{"changed reason", func(r agentmail.FileReservation) agentmail.FileReservation { r.Reason = "replacement"; return r }},
		{"changed creation", func(r agentmail.FileReservation) agentmail.FileReservation {
			r.CreatedTS.Time = r.CreatedTS.Add(time.Nanosecond)
			return r
		}},
		{"missing creation", func(r agentmail.FileReservation) agentmail.FileReservation { r.CreatedTS.Time = time.Time{}; return r }},
		{"expired", func(r agentmail.FileReservation) agentmail.FileReservation {
			r.ExpiresTS.Time = time.Now().Add(-time.Hour)
			return r
		}},
		{"missing expiry", func(r agentmail.FileReservation) agentmail.FileReservation { r.ExpiresTS.Time = time.Time{}; return r }},
		{"released", func(r agentmail.FileReservation) agentmail.FileReservation {
			r.ReleasedTS = &agentmail.FlexTime{Time: time.Now()}
			return r
		}},
		{"zero released timestamp", func(r agentmail.FileReservation) agentmail.FileReservation {
			r.ReleasedTS = &agentmail.FlexTime{}
			return r
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, sameAgent := range []bool{false, true} {
				client, opts := sourceLeaseFixture()
				if sameAgent {
					opts.ToAgent = opts.FromAgent
				}
				// a.go is valid and sorted first: a later failure must still
				// prevent release/renewal of the entire batch, including a.go.
				row := tc.change(client.live[22])
				delete(client.live, 22)
				client.live[row.ID] = row
				before, _ := json.Marshal(client.live)
				result, err := TransferReservations(context.Background(), client, opts)
				if !errors.Is(err, ErrTransferSourceEvidence) || result.Success || result.OutcomeUnknown || result.Stage != "verify_source" ||
					client.reads != 1 || len(client.mutations) != 0 || client.acquisitions != 0 {
					t.Fatalf("stale source reached mutation (same=%v): %+v %v calls=%v", sameAgent, result, err, client.mutations)
				}
				after, _ := json.Marshal(client.live)
				if string(before) != string(after) {
					t.Fatal("preflight changed the registry")
				}
			}
		})
	}
}

func TestTransferSourceRejectsUnavailableOrInconsistentListing(t *testing.T) {
	readFailure := errors.New("source listing unavailable")
	for _, mode := range []string{"absent", "duplicate", "missing ID", "read failure", "cancellation"} {
		t.Run(mode, func(t *testing.T) {
			client, opts := sourceLeaseFixture()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "absent":
				client.listing = []agentmail.FileReservation{}
			case "duplicate":
				client.listing = []agentmail.FileReservation{client.live[11], client.live[22], client.live[22]}
			case "missing ID":
				client.listing = []agentmail.FileReservation{client.live[11], client.live[22], {}}
			case "read failure":
				client.listErr = readFailure
			case "cancellation":
				client.afterRead = cancel
			}
			result, err := TransferReservations(ctx, client, opts)
			if !errors.Is(err, ErrTransferSourceEvidence) || result.Success || result.OutcomeUnknown || client.reads != 1 || len(client.mutations) != 0 || client.acquisitions != 0 {
				t.Fatalf("bad listing authorized mutation: %+v %v calls=%v", result, err, client.mutations)
			}
			if mode == "read failure" && !errors.Is(err, readFailure) || mode == "cancellation" && !errors.Is(err, context.Canceled) {
				t.Fatalf("source failure cause was discarded: %v", err)
			}
		})
	}
}

func TestTransferSourceReplacementBetweenReadAndMutationSurvives(t *testing.T) {
	for _, sameAgent := range []bool{false, true} {
		t.Run(map[bool]string{false: "release", true: "renew"}[sameAgent], func(t *testing.T) {
			client, opts := sourceLeaseFixture()
			if sameAgent {
				opts.ToAgent = opts.FromAgent
			}
			var replaced []byte
			client.beforeMutation = func() {
				for _, id := range []int{11, 22} {
					row := client.live[id]
					delete(client.live, id)
					row.ID += 100
					row.CreatedTS.Time = row.CreatedTS.Add(time.Minute)
					client.live[row.ID] = row
				}
				replaced, _ = json.Marshal(client.live)
			}
			result, err := TransferReservations(context.Background(), client, opts)
			after, _ := json.Marshal(client.live)
			if string(after) != string(replaced) {
				t.Fatalf("a stale handoff modified newer same-path leases: before=%s after=%s", replaced, after)
			}
			if err == nil || result.Success || !result.OutcomeUnknown || result.RolledBack || result.Attempts != 0 || client.acquisitions != 0 {
				t.Fatalf("uncertain source mutation authorized acquisition or compensation: %+v %v", result, err)
			}
			if len(client.mutations) != 1 || len(client.mutations[0].paths) != 0 || !reflect.DeepEqual(client.mutations[0].ids, []int{11, 22}) {
				t.Fatalf("source operation escaped its captured lease IDs: %+v", client.mutations)
			}
		})
	}
}

func TestTransferSourceAllowsRenewedExpiryButNotChangedMode(t *testing.T) {
	client, opts := sourceLeaseFixture()
	for i := range opts.Reservations {
		opts.Reservations[i].ExpiresAt = opts.Reservations[i].CreatedAt.Add(time.Hour) // Captured expiry is old; exact live lease was renewed.
		opts.Reservations[i].CreatedAt = opts.Reservations[i].CreatedAt.In(time.FixedZone("other", -5*60*60))
	}
	opts.ToAgent = opts.FromAgent
	result, err := TransferReservations(context.Background(), client, opts)
	if err != nil || !result.Success || client.reads != 1 {
		t.Fatalf("renewed lease with unchanged identity rejected: %+v %v", result, err)
	}
	client, opts = sourceLeaseFixture()
	row := client.live[11]
	row.Exclusive = false
	client.live[11] = row
	result, err = TransferReservations(context.Background(), client, opts)
	if !errors.Is(err, ErrTransferSourceEvidence) || result.Success || len(client.mutations) != 0 {
		t.Fatalf("exclusive-to-shared mode drift allowed: %+v %v", result, err)
	}
}

func TestTransferSourceUncertainMutationCannotAuthorizeAcquisition(t *testing.T) {
	for _, kind := range []string{"release", "renew"} {
		for _, count := range []int{0, 1, 3} {
			client, opts := sourceLeaseFixture()
			if kind == "renew" {
				opts.ToAgent = opts.FromAgent
			}
			client.ackCount = &count
			result, err := TransferReservations(context.Background(), client, opts)
			if err == nil || result.Success || !result.OutcomeUnknown || result.RolledBack || result.Stage != kind || client.acquisitions != 0 || result.Attempts != 0 {
				t.Fatalf("%s acknowledged %d, allowed further work: %+v %v", kind, count, result, err)
			}
			wire, _ := json.Marshal(result)
			var evidence struct {
				Released []int `json:"released_ids"`
			}
			if err := json.Unmarshal(wire, &evidence); err != nil || len(evidence.Released) != 0 {
				t.Fatalf("uncertain release fabricated confirmed IDs: %s %v", wire, err)
			}
		}
	}
}
