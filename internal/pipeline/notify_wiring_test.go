package pipeline

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Pipeline notifications were documented (docs/WORKFLOW_SCHEMA.md notify_channels
// / webhook_url, docs/ORCHESTRATION_FEATURES.md "Pipeline Notifications") and
// fully implemented, but nothing outside tests ever called SetNotifier, so the
// executor's notifier was always nil and every notification was dropped. The
// existing tests all injected a notifier by hand and so could not see it. These
// drive the path a real workflow takes: settings only, no injection.

func notifyTestWorkflow(channels []string, webhookURL string) *Workflow {
	return &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "notify-wiring",
		Settings: WorkflowSettings{
			NotifyOnComplete: true,
			NotifyOnError:    true,
			NotifyChannels:   channels,
			WebhookURL:       webhookURL,
		},
	}
}

func notifyTestExecutor(t *testing.T) *Executor {
	t.Helper()
	e := NewExecutor(DefaultExecutorConfig("notify-session"))
	e.state = &ExecutionState{
		RunID:      "notify-run",
		WorkflowID: "notify-wiring",
		Status:     StatusCompleted,
		StartedAt:  time.Now().Add(-time.Second),
		FinishedAt: time.Now(),
		Steps:      make(map[string]StepResult),
	}
	return e
}

func TestWorkflowSettingsAloneDeliverWebhookNotification(t *testing.T) {
	received := make(chan NotificationPayload, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload NotificationPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode webhook payload: %v", err)
		}
		received <- payload
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	e := notifyTestExecutor(t)
	workflow := notifyTestWorkflow([]string{"webhook"}, server.URL)

	// No SetNotifier call anywhere: the workflow's own settings must be enough.
	e.deliverNotification(e.prepareNotification(workflow, NotifyCompleted))

	select {
	case payload := <-received:
		if payload.Event != NotifyCompleted {
			t.Errorf("payload.Event = %q, want %q", payload.Event, NotifyCompleted)
		}
		if payload.WorkflowName != workflow.Name {
			t.Errorf("payload.WorkflowName = %q, want %q", payload.WorkflowName, workflow.Name)
		}
		if payload.RunID != e.state.RunID {
			t.Errorf("payload.RunID = %q, want %q", payload.RunID, e.state.RunID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("workflow declared notify_channels: [webhook] but no webhook request arrived")
	}
}

// A workflow that asks for nothing must stay silent — the overwhelmingly common
// case, and the one that must not regress into per-run network calls.
func TestNotifierForSettingsNilWithoutChannels(t *testing.T) {
	e := notifyTestExecutor(t)

	if got := e.notifierForSettings(WorkflowSettings{NotifyOnComplete: true}); got != nil {
		t.Errorf("no notify_channels should yield no notifier, got %+v", got)
	}
	if got := e.prepareNotification(notifyTestWorkflow(nil, ""), NotifyCompleted); got != nil {
		t.Errorf("no notify_channels should prepare nothing, got %+v", got)
	}
}

// ShouldNotify still governs: channels configured but the event switched off.
func TestPrepareNotificationRespectsShouldNotify(t *testing.T) {
	e := notifyTestExecutor(t)
	workflow := notifyTestWorkflow([]string{"webhook"}, "http://127.0.0.1:1")
	workflow.Settings.NotifyOnComplete = false

	if got := e.prepareNotification(workflow, NotifyCompleted); got != nil {
		t.Errorf("notify_on_complete=false must prepare nothing, got %+v", got)
	}
}

func TestNotifierForSettingsIgnoresUnknownChannels(t *testing.T) {
	e := notifyTestExecutor(t)

	if got := e.notifierForSettings(WorkflowSettings{NotifyChannels: []string{"carrier-pigeon"}}); got != nil {
		t.Errorf("an unrecognized channel should yield no notifier, got %+v", got)
	}
	n := e.notifierForSettings(WorkflowSettings{NotifyChannels: []string{"carrier-pigeon", "webhook"}})
	if n == nil {
		t.Fatal("a recognized channel alongside an unknown one should still notify")
	}
	if len(n.channels) != 1 || n.channels[0] != ChannelWebhook {
		t.Errorf("channels = %v, want [%v]", n.channels, ChannelWebhook)
	}
}

// A mail_recipient left in the settings must not drag an Agent Mail client into
// a workflow that only asked for a webhook.
func TestNotifierForSettingsAttachesMailClientOnlyForMailChannel(t *testing.T) {
	e := notifyTestExecutor(t)

	webhookOnly := e.notifierForSettings(WorkflowSettings{
		NotifyChannels: []string{"webhook"},
		WebhookURL:     "http://127.0.0.1:1",
		MailRecipient:  "someone",
	})
	if webhookOnly == nil {
		t.Fatal("webhook channel should yield a notifier")
	}
	if webhookOnly.mailClient != nil {
		t.Error("mail client built for a workflow that did not request the mail channel")
	}

	withMail := e.notifierForSettings(WorkflowSettings{
		NotifyChannels: []string{"mail"},
		MailRecipient:  "someone",
	})
	if withMail == nil {
		t.Fatal("mail channel should yield a notifier")
	}
	if withMail.mailClient == nil {
		t.Error("mail channel with a recipient should have a mail client attached")
	}
	if withMail.agentName != notifierSenderName {
		t.Errorf("agentName = %q, want %q", withMail.agentName, notifierSenderName)
	}
}

// An explicitly injected notifier still wins, so existing callers of SetNotifier
// keep their override.
func TestSetNotifierOverridesWorkflowSettings(t *testing.T) {
	e := notifyTestExecutor(t)
	injected := NewNotifier(NotifierConfig{Channels: []string{"webhook"}, WebhookURL: "http://injected.invalid"})
	e.SetNotifier(injected)

	pending := e.prepareNotification(notifyTestWorkflow([]string{"desktop"}, ""), NotifyCompleted)
	if pending == nil {
		t.Fatal("prepareNotification returned nil with an injected notifier")
	}
	if pending.notifier != injected {
		t.Error("an injected notifier must take precedence over workflow settings")
	}
}

func TestPrepareNotificationNilSafe(t *testing.T) {
	e := notifyTestExecutor(t)
	if got := e.prepareNotification(nil, NotifyCompleted); got != nil {
		t.Errorf("nil workflow must prepare nothing, got %+v", got)
	}

	e.state = nil
	if got := e.prepareNotification(notifyTestWorkflow([]string{"webhook"}, "http://127.0.0.1:1"), NotifyCompleted); got != nil {
		t.Errorf("nil state must prepare nothing rather than panic, got %+v", got)
	}

	e.deliverNotification(nil) // must not panic
}

// Delivery leaves the process under a 10s budget. Holding stateMu across it would
// stall progress emission, status reads and cancellation for as long as a slow
// webhook takes to answer — so the lock must already be released by then.
func TestDeliverNotificationDoesNotHoldStateLock(t *testing.T) {
	inFlight := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(inFlight)
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	e := notifyTestExecutor(t)
	workflow := notifyTestWorkflow([]string{"webhook"}, server.URL)

	e.stateMu.Lock()
	pending := e.prepareNotification(workflow, NotifyCompleted)
	e.stateMu.Unlock()
	if pending == nil {
		t.Fatal("prepareNotification returned nil")
	}

	delivered := make(chan struct{})
	go func() {
		e.deliverNotification(pending)
		close(delivered)
	}()

	<-inFlight // the webhook is mid-flight; stateMu must be free

	acquired := make(chan struct{})
	go func() {
		e.stateMu.Lock()
		e.stateMu.Unlock() //nolint:staticcheck // acquiring is the assertion
		close(acquired)
	}()

	select {
	case <-acquired:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("stateMu was held while a notification was being delivered")
	}

	close(release)
	<-delivered
}
