package agentmail

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/bd"
)

type fakeAMClient struct {
	available      bool
	inboxResponses [][]InboxMessage
	inboxErrors    []error
	fetchCalls     int

	sendCalls []SendMessageOptions
	sendErr   error

	readCalls []int
	ackCalls  []int
}

func (f *fakeAMClient) IsAvailable() bool {
	return f.available
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

func (f *fakeAMClient) MarkMessageRead(ctx context.Context, projectKey, agentName string, messageID int) error {
	f.readCalls = append(f.readCalls, messageID)
	return nil
}

func (f *fakeAMClient) AcknowledgeMessage(ctx context.Context, projectKey, agentName string, messageID int) error {
	f.ackCalls = append(f.ackCalls, messageID)
	return nil
}

type fakeBDClient struct {
	inbox    []bd.Message
	inboxErr error

	sendCalls []bdSendCall
	sendErr   error

	readMessages map[string]*bd.Message
	readErr      error

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
	return f.inbox, f.inboxErr
}

func (f *fakeBDClient) Read(ctx context.Context, id string) (*bd.Message, error) {
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
	if len(am.readCalls) != 1 || am.readCalls[0] != 42 {
		t.Fatalf("expected MarkMessageRead called with 42, got %+v", am.readCalls)
	}
}

func TestUnifiedMessengerRead_AgentMailFetchesDeeperHistory(t *testing.T) {
	now := time.Now()
	am := &fakeAMClient{
		available: true,
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
