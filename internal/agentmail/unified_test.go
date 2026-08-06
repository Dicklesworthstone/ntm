package agentmail

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/bd"
)

type fakeAMClient struct {
	available           bool
	availabilityCalls   int
	availabilityStarted chan struct{}
	inboxResponses      [][]InboxMessage
	inboxErrors         []error
	fetchCalls          int

	sendCalls []SendMessageOptions
	sendErr   error

	readCalls []int
	markErr   error
	ackCalls  []int
	ackErr    error

	getMessageCalls []int
	getMessageErr   error
}

func (f *fakeAMClient) IsAvailableContext(ctx context.Context) bool {
	f.availabilityCalls++
	if f.availabilityStarted != nil {
		select {
		case f.availabilityStarted <- struct{}{}:
		case <-ctx.Done():
			return false
		}
		<-ctx.Done()
		return false
	}
	return f.available
}

func (f *fakeAMClient) GetMessage(ctx context.Context, projectKey string, messageID int) (*Message, error) {
	f.getMessageCalls = append(f.getMessageCalls, messageID)
	if f.getMessageErr != nil {
		return nil, f.getMessageErr
	}

	// Try to find in inbox responses
	for _, response := range f.inboxResponses {
		for _, msg := range response {
			if msg.ID == messageID {
				return &Message{
					ID:        msg.ID,
					From:      msg.From,
					Subject:   msg.Subject,
					BodyMD:    msg.BodyMD,
					CreatedTS: msg.CreatedTS,
				}, nil
			}
		}
	}

	return nil, ErrMessageNotFound
}

func (f *fakeAMClient) FetchInbox(ctx context.Context, opts FetchInboxOptions) ([]InboxMessage, error) {
	f.fetchCalls++
	if len(f.inboxResponses) == 0 {
		if len(f.inboxErrors) > 0 {
			return nil, f.inboxErrors[0]
		}
		return nil, nil
	}
	idx := f.fetchCalls - 1
	if idx >= len(f.inboxResponses) {
		idx = len(f.inboxResponses) - 1
	}
	var err error
	if len(f.inboxErrors) > 0 {
		if idx < len(f.inboxErrors) {
			err = f.inboxErrors[idx]
		} else {
			err = f.inboxErrors[len(f.inboxErrors)-1]
		}
	}
	return f.inboxResponses[idx], err
}

func (f *fakeAMClient) SendMessage(ctx context.Context, opts SendMessageOptions) (*SendResult, error) {
	f.sendCalls = append(f.sendCalls, opts)
	if f.sendErr != nil {
		return nil, f.sendErr
	}
	return &SendResult{}, nil
}

func (f *fakeAMClient) MarkMessageRead(ctx context.Context, projectKey, agentName string, messageID int) (*MessageReadResult, error) {
	f.readCalls = append(f.readCalls, messageID)
	if f.markErr != nil {
		return nil, f.markErr
	}
	return &MessageReadResult{MessageID: messageID, Read: true}, nil
}

func (f *fakeAMClient) AcknowledgeMessage(ctx context.Context, projectKey, agentName string, messageID int) (*MessageAckResult, error) {
	f.ackCalls = append(f.ackCalls, messageID)
	if f.ackErr != nil {
		return nil, f.ackErr
	}
	return &MessageAckResult{MessageID: messageID, Acknowledged: true}, nil
}

type fakeBDClient struct {
	inbox      []bd.Message
	inboxErr   error
	inboxCalls int

	sendCalls []bdSendCall
	sendErr   error

	readMessages map[string]*bd.Message
	readErr      error
	readCalls    []string

	ackCalls []string
	ackErr   error
}

type bdSendCall struct {
	to   string
	body string
}

func (f *fakeBDClient) Send(ctx context.Context, to, body string) error {
	f.sendCalls = append(f.sendCalls, bdSendCall{to: to, body: body})
	return f.sendErr
}

func (f *fakeBDClient) Inbox(ctx context.Context, unreadOnly, urgentOnly bool) ([]bd.Message, error) {
	f.inboxCalls++
	return f.inbox, f.inboxErr
}

func (f *fakeBDClient) Read(ctx context.Context, id string) (*bd.Message, error) {
	f.readCalls = append(f.readCalls, id)
	if f.readErr != nil {
		return nil, f.readErr
	}
	msg, ok := f.readMessages[id]
	if !ok {
		return nil, ErrMessageNotFound
	}
	return msg, nil
}

func (f *fakeBDClient) Ack(ctx context.Context, id string) error {
	f.ackCalls = append(f.ackCalls, id)
	return f.ackErr
}

func TestUnifiedMessengerInbox_MergesAndSorts(t *testing.T) {
	now := time.Now()
	am := &fakeAMClient{
		available: true,
		inboxResponses: [][]InboxMessage{{
			{ID: 1, From: "alice", Subject: "AM-1", BodyMD: "hello", CreatedTS: FlexTime{now.Add(-2 * time.Minute)}},
			{ID: 2, From: "bob", Subject: "AM-2", BodyMD: "world", CreatedTS: FlexTime{now.Add(-4 * time.Minute)}},
		}},
	}
	bdClient := &fakeBDClient{
		inbox: []bd.Message{
			{ID: "99", From: "charlie", Body: "bd-msg", Timestamp: now.Add(-1 * time.Minute)},
		},
	}

	unified := &UnifiedMessenger{
		amClient:   am,
		bdClient:   bdClient,
		projectKey: "proj",
		agentName:  "agent",
	}

	msgs, err := unified.Inbox(context.Background())
	if err != nil {
		t.Fatalf("Inbox() error: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("Inbox() returned %d messages, want 3", len(msgs))
	}
	if msgs[0].ID != "bd-99" || msgs[0].Channel != "bd" {
		t.Fatalf("expected newest message to be bd-99, got %s (%s)", msgs[0].ID, msgs[0].Channel)
	}
	if msgs[1].ID != "am-1" || msgs[2].ID != "am-2" {
		t.Fatalf("unexpected order: %s, %s", msgs[1].ID, msgs[2].ID)
	}
}

func TestUnifiedMessengerSend_PrefersAgentMail(t *testing.T) {
	am := &fakeAMClient{available: true}
	bdClient := &fakeBDClient{}

	unified := &UnifiedMessenger{
		amClient:   am,
		bdClient:   bdClient,
		projectKey: "proj",
		agentName:  "agent",
	}

	if err := unified.Send(context.Background(), "target", "subject", "body"); err != nil {
		t.Fatalf("Send() error: %v", err)
	}
	if len(am.sendCalls) != 1 {
		t.Fatalf("expected 1 agent mail send call, got %d", len(am.sendCalls))
	}
	if len(bdClient.sendCalls) != 0 {
		t.Fatalf("expected BD send not called, got %d", len(bdClient.sendCalls))
	}
}

func TestUnifiedMessengerSend_FallsBackToBDWhenNoAgentMail(t *testing.T) {
	bdClient := &fakeBDClient{}
	unified := &UnifiedMessenger{
		bdClient:   bdClient,
		projectKey: "proj",
		agentName:  "agent",
	}

	if err := unified.Send(context.Background(), "target", "subject", "body"); err != nil {
		t.Fatalf("Send() error: %v", err)
	}
	if len(bdClient.sendCalls) != 1 {
		t.Fatalf("expected BD send call, got %d", len(bdClient.sendCalls))
	}
}

func TestUnifiedMessengerRead_AgentMailMarksRead(t *testing.T) {
	now := time.Now()
	am := &fakeAMClient{
		available: true,
		inboxResponses: [][]InboxMessage{{
			{ID: 42, From: "alice", Subject: "hello", BodyMD: "body", CreatedTS: FlexTime{now}},
		}},
	}

	unified := &UnifiedMessenger{
		amClient:   am,
		projectKey: "proj",
		agentName:  "agent",
	}

	msg, err := unified.Read(context.Background(), "am-42")
	if err != nil {
		t.Fatalf("Read() error: %v", err)
	}
	if msg.ID != "am-42" || msg.Channel != "agentmail" {
		t.Fatalf("unexpected message: %+v", msg)
	}
	if len(am.getMessageCalls) != 1 || am.getMessageCalls[0] != 42 {
		t.Fatalf("expected GetMessage called with 42, got %+v", am.getMessageCalls)
	}
	if len(am.readCalls) != 1 || am.readCalls[0] != 42 {
		t.Fatalf("expected MarkMessageRead called with 42, got %+v", am.readCalls)
	}
}

func TestUnifiedMessengerRead_AgentMailFetchesDeeperHistory(t *testing.T) {
	now := time.Now()
	am := &fakeAMClient{
		available: true,
		// Force GetMessage to fail to test the fallback inbox scanning logic
		getMessageErr: errors.New("not implemented"),
		inboxResponses: [][]InboxMessage{
			{{ID: 1, From: "skip", Subject: "skip", BodyMD: "skip", CreatedTS: FlexTime{now}}},
			{{ID: 99, From: "found", Subject: "ok", BodyMD: "ok", CreatedTS: FlexTime{now}}},
		},
	}

	unified := &UnifiedMessenger{
		amClient:   am,
		projectKey: "proj",
		agentName:  "agent",
	}

	msg, err := unified.Read(context.Background(), "am-99")
	if err != nil {
		t.Fatalf("Read() error: %v", err)
	}
	if msg.ID != "am-99" {
		t.Fatalf("expected am-99, got %s", msg.ID)
	}
	if am.fetchCalls != 2 {
		t.Fatalf("expected 2 FetchInbox calls, got %d", am.fetchCalls)
	}
}

func TestUnifiedMessengerRead_AgentMailPreservesDeeperHistoryFailure(t *testing.T) {
	historyErr := errors.New("history unavailable")
	am := &fakeAMClient{
		available:     true,
		getMessageErr: errors.New("not implemented"),
		inboxResponses: [][]InboxMessage{
			{{ID: 1, From: "skip", Subject: "skip", BodyMD: "skip"}},
			nil,
		},
		inboxErrors: []error{nil, historyErr},
	}

	unified := &UnifiedMessenger{
		amClient:   am,
		projectKey: "proj",
		agentName:  "agent",
	}

	_, err := unified.Read(context.Background(), "am-99")
	if !errors.Is(err, historyErr) {
		t.Fatalf("Read() error = %v, want wrapped %v", err, historyErr)
	}
	if am.fetchCalls != 2 {
		t.Fatalf("expected 2 FetchInbox calls, got %d", am.fetchCalls)
	}
}

func TestUnifiedMessengerAck_BD(t *testing.T) {
	bdClient := &fakeBDClient{}
	unified := &UnifiedMessenger{
		bdClient:   bdClient,
		projectKey: "proj",
		agentName:  "agent",
	}

	if err := unified.Ack(context.Background(), "bd-abc"); err != nil {
		t.Fatalf("Ack() error: %v", err)
	}
	if len(bdClient.ackCalls) != 1 || bdClient.ackCalls[0] != "abc" {
		t.Fatalf("expected BD ack for abc, got %+v", bdClient.ackCalls)
	}
}

func TestUnifiedMessengerSend_FallsBackToBDWhenAgentMailFails(t *testing.T) {
	am := &fakeAMClient{available: true, sendErr: errors.New("send failed")}
	bdClient := &fakeBDClient{}

	unified := &UnifiedMessenger{
		amClient:   am,
		bdClient:   bdClient,
		projectKey: "proj",
		agentName:  "agent",
	}

	if err := unified.Send(context.Background(), "target", "subject", "body"); err != nil {
		t.Fatalf("Send() error: %v", err)
	}
	if len(bdClient.sendCalls) != 1 {
		t.Fatalf("expected BD send called on agent mail error, got %d", len(bdClient.sendCalls))
	}
}

func TestUnifiedMessengerRead_InvalidID(t *testing.T) {
	unified := &UnifiedMessenger{}
	if _, err := unified.Read(context.Background(), "x"); err == nil {
		t.Fatal("expected error for invalid message id")
	}
	if _, err := unified.Read(context.Background(), "zz-123"); err == nil {
		t.Fatal("expected error for unknown message channel")
	}
}

func TestUnifiedMessengerRead_RejectsMalformedChannelPrefixes(t *testing.T) {
	am := &fakeAMClient{available: true}
	bdClient := &fakeBDClient{readMessages: map[string]*bd.Message{"abc": {ID: "abc"}}}
	unified := &UnifiedMessenger{amClient: am, bdClient: bdClient}

	for _, id := range []string{"amx42", "bdxabc"} {
		if _, err := unified.Read(context.Background(), id); err == nil {
			t.Fatalf("Read(%q) error = nil, want malformed ID error", id)
		}
	}
	if len(am.getMessageCalls) != 0 {
		t.Fatalf("GetMessage called for malformed ID: %+v", am.getMessageCalls)
	}
	if len(bdClient.readCalls) != 0 {
		t.Fatalf("BD Read called for malformed ID: %+v", bdClient.readCalls)
	}
}

func TestUnifiedMessengerRead_BD(t *testing.T) {
	bdClient := &fakeBDClient{
		readMessages: map[string]*bd.Message{
			"abc": {ID: "abc", From: "delta", Body: "hi", Timestamp: time.Now()},
		},
	}
	unified := &UnifiedMessenger{
		bdClient:   bdClient,
		projectKey: "proj",
		agentName:  "agent",
	}

	msg, err := unified.Read(context.Background(), "bd-abc")
	if err != nil {
		t.Fatalf("Read() error: %v", err)
	}
	if msg.Channel != "bd" || msg.ID != "bd-abc" {
		t.Fatalf("unexpected message: %+v", msg)
	}
}

func TestUnifiedMessengerAck_AgentMail(t *testing.T) {
	am := &fakeAMClient{available: true}
	unified := &UnifiedMessenger{
		amClient:   am,
		projectKey: "proj",
		agentName:  "agent",
	}

	if err := unified.Ack(context.Background(), "am-7"); err != nil {
		t.Fatalf("Ack() error: %v", err)
	}
	if len(am.ackCalls) != 1 || am.ackCalls[0] != 7 {
		t.Fatalf("expected agent mail ack for 7, got %+v", am.ackCalls)
	}
}

func TestUnifiedMessengerAck_InvalidID(t *testing.T) {
	unified := &UnifiedMessenger{}
	if err := unified.Ack(context.Background(), "bad"); err == nil {
		t.Fatal("expected error for invalid id")
	}
	if err := unified.Ack(context.Background(), "zz-123"); err == nil {
		t.Fatal("expected error for unknown channel")
	}
}

func TestUnifiedMessengerAck_RejectsMalformedChannelPrefixes(t *testing.T) {
	am := &fakeAMClient{available: true}
	bdClient := &fakeBDClient{}
	unified := &UnifiedMessenger{amClient: am, bdClient: bdClient}

	for _, id := range []string{"amx42", "bdxabc"} {
		if err := unified.Ack(context.Background(), id); err == nil {
			t.Fatalf("Ack(%q) error = nil, want malformed ID error", id)
		}
	}
	if len(am.ackCalls) != 0 {
		t.Fatalf("Agent Mail acknowledge called for malformed ID: %+v", am.ackCalls)
	}
	if len(bdClient.ackCalls) != 0 {
		t.Fatalf("BD acknowledge called for malformed ID: %+v", bdClient.ackCalls)
	}
}

func TestUnifiedMessengerRejectsPreCanceledContextsBeforeChannelWork(t *testing.T) {
	tests := []struct {
		name string
		call func(*UnifiedMessenger, context.Context) error
	}{
		{
			name: "inbox",
			call: func(m *UnifiedMessenger, ctx context.Context) error {
				_, err := m.Inbox(ctx)
				return err
			},
		},
		{
			name: "send",
			call: func(m *UnifiedMessenger, ctx context.Context) error {
				return m.Send(ctx, "target", "subject", "body")
			},
		},
		{
			name: "read",
			call: func(m *UnifiedMessenger, ctx context.Context) error {
				_, err := m.Read(ctx, "am-1")
				return err
			},
		},
		{
			name: "ack",
			call: func(m *UnifiedMessenger, ctx context.Context) error {
				return m.Ack(ctx, "am-1")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			am := &fakeAMClient{available: true}
			bdClient := &fakeBDClient{}
			messenger := &UnifiedMessenger{
				amClient:   am,
				bdClient:   bdClient,
				projectKey: "proj",
				agentName:  "agent",
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			err := tt.call(messenger, ctx)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("operation error = %v, want context.Canceled", err)
			}
			assertNoUnifiedChannelCalls(t, am, bdClient)
		})
	}
}

func TestUnifiedMessengerRejectsNilContextsBeforeChannelWork(t *testing.T) {
	tests := []struct {
		name string
		call func(*UnifiedMessenger) error
	}{
		{
			name: "inbox",
			call: func(m *UnifiedMessenger) error {
				_, err := m.Inbox(nil)
				return err
			},
		},
		{
			name: "send",
			call: func(m *UnifiedMessenger) error {
				return m.Send(nil, "target", "subject", "body")
			},
		},
		{
			name: "read",
			call: func(m *UnifiedMessenger) error {
				_, err := m.Read(nil, "am-1")
				return err
			},
		},
		{
			name: "ack",
			call: func(m *UnifiedMessenger) error {
				return m.Ack(nil, "am-1")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			am := &fakeAMClient{available: true}
			bdClient := &fakeBDClient{}
			messenger := &UnifiedMessenger{
				amClient:   am,
				bdClient:   bdClient,
				projectKey: "proj",
				agentName:  "agent",
			}

			if err := tt.call(messenger); err == nil {
				t.Fatal("operation succeeded with a nil context")
			}
			assertNoUnifiedChannelCalls(t, am, bdClient)
		})
	}
}

func TestUnifiedMessengerSend_CancelDuringAvailabilityDoesNotFallBack(t *testing.T) {
	started := make(chan struct{})
	am := &fakeAMClient{
		available:           true,
		availabilityStarted: started,
	}
	bdClient := &fakeBDClient{}
	messenger := &UnifiedMessenger{
		amClient:   am,
		bdClient:   bdClient,
		projectKey: "proj",
		agentName:  "agent",
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- messenger.Send(ctx, "target", "subject", "body")
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("availability check did not start")
	}
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Send() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Send() did not return after cancellation")
	}
	if len(am.sendCalls) != 0 {
		t.Fatalf("agent mail send called after canceled availability check: %d", len(am.sendCalls))
	}
	if len(bdClient.sendCalls) != 0 {
		t.Fatalf("BD fallback called after cancellation: %d", len(bdClient.sendCalls))
	}
}

func TestUnifiedMessengerInbox_PreservesFetchCancellation(t *testing.T) {
	am := &fakeAMClient{available: true, inboxErrors: []error{context.Canceled}}
	bdClient := &fakeBDClient{}
	messenger := &UnifiedMessenger{amClient: am, bdClient: bdClient}

	_, err := messenger.Inbox(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Inbox() error = %v, want context.Canceled", err)
	}
	if bdClient.inboxCalls != 0 {
		t.Fatalf("BD fallback called after canceled inbox fetch: %d", bdClient.inboxCalls)
	}
}

func TestUnifiedMessengerSend_PreservesSendCancellation(t *testing.T) {
	am := &fakeAMClient{available: true, sendErr: context.DeadlineExceeded}
	bdClient := &fakeBDClient{}
	messenger := &UnifiedMessenger{amClient: am, bdClient: bdClient}

	err := messenger.Send(context.Background(), "target", "subject", "body")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Send() error = %v, want context.DeadlineExceeded", err)
	}
	if len(bdClient.sendCalls) != 0 {
		t.Fatalf("BD fallback called after canceled send: %d", len(bdClient.sendCalls))
	}
}

func TestUnifiedMessengerRead_PreservesGetCancellation(t *testing.T) {
	am := &fakeAMClient{available: true, getMessageErr: context.Canceled}
	messenger := &UnifiedMessenger{amClient: am}

	_, err := messenger.Read(context.Background(), "am-42")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Read() error = %v, want context.Canceled", err)
	}
	if am.fetchCalls != 0 {
		t.Fatalf("inbox fallback called after canceled get: %d", am.fetchCalls)
	}
}

func TestUnifiedMessengerAck_PreservesAcknowledgeCancellation(t *testing.T) {
	am := &fakeAMClient{available: true, ackErr: context.DeadlineExceeded}
	messenger := &UnifiedMessenger{amClient: am}

	err := messenger.Ack(context.Background(), "am-7")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Ack() error = %v, want context.DeadlineExceeded", err)
	}
}

func assertNoUnifiedChannelCalls(t *testing.T, am *fakeAMClient, bdClient *fakeBDClient) {
	t.Helper()
	if am.availabilityCalls != 0 || am.fetchCalls != 0 || len(am.sendCalls) != 0 ||
		len(am.getMessageCalls) != 0 || len(am.readCalls) != 0 || len(am.ackCalls) != 0 {
		t.Fatalf("agent mail was called: %+v", am)
	}
	if bdClient.inboxCalls != 0 || len(bdClient.sendCalls) != 0 ||
		len(bdClient.readCalls) != 0 || len(bdClient.ackCalls) != 0 {
		t.Fatalf("BD was called: %+v", bdClient)
	}
}
