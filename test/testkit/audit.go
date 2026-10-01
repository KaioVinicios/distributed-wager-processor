package testkit

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// AuditTimeout bounds how long WaitFor waits for the events.
const AuditTimeout = 10 * time.Second

// receiveTimeout bounds one receive of Absent: well above its 1 s long poll.
const receiveTimeout = 5 * time.Second

// auditVisibility (seconds) hides a received batch only briefly: a batch the
// Audit did not record or delete (a response lost and retried by the SDK, a
// delete that did not arrive) comes back within AuditTimeout instead of
// blocking its FIFO group for the queue's 30 s (spec audit-lost-receive).
const auditVisibility = 2

// Attribute is one message attribute of a delivery.
type Attribute struct {
	Type  string // String or Number
	Value string
}

// AuditMessage is one delivery read from the audit queue.
type AuditMessage struct {
	Body        []byte
	GroupID     string
	DedupID     string
	Attributes  map[string]Attribute
	ContractErr error // the body violates api/events.yaml
}

// Audit collects the audit queue of one events topic. Every message is kept
// once by eventId, so tests running in parallel over the same queue each find
// their own events, and each message is checked against the event contract
// (spec M4, decision 17). A message the broker delivers again (its delete did
// not arrive) is not a new delivery; a new message from the topic is (spec
// audit-lost-receive, decision 2). Messages are deleted from the queue as soon
// as they are read.
type Audit struct {
	client   *sqs.Client
	url      string
	contract *EventContract

	mu   sync.Mutex
	byID map[string][]AuditMessage // "" keeps bodies without a readable eventId
	seen map[string]struct{}       // SQS MessageId of the messages kept
}

// NewAudit reads queueURL and validates every delivery against the event contract.
func NewAudit(ctx context.Context, client *sqs.Client, queueURL string) (*Audit, error) {
	contract, err := LoadEventContract(ctx)
	if err != nil {
		return nil, err
	}
	return &Audit{client: client, url: queueURL, contract: contract, byID: map[string][]AuditMessage{}, seen: map[string]struct{}{}}, nil
}

// Wait receives until every id has at least one delivery, or ctx ends. It
// returns every delivery seen so far of the ids.
func (a *Audit) Wait(ctx context.Context, ids ...string) (map[string][]AuditMessage, error) {
	for {
		got, missing := a.lookup(ids)
		if len(missing) == 0 {
			return got, nil
		}
		if err := a.receive(ctx); err != nil {
			return got, fmt.Errorf("audit: %d of %d events not delivered (first: %v): %w",
				len(missing), len(ids), missing[:min(3, len(missing))], err)
		}
	}
}

// WaitFor is Wait with AuditTimeout, failing tb on a missing id or on a
// delivery off the contract. It detaches from the test's context, so it also
// works in t.Cleanup.
func (a *Audit) WaitFor(tb testing.TB, ids ...string) map[string][]AuditMessage {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(tb.Context()), AuditTimeout)
	defer cancel()
	got, err := a.Wait(ctx, ids...)
	if err != nil {
		tb.Fatal(err)
	}
	for id, deliveries := range got {
		for _, m := range deliveries {
			if m.ContractErr != nil {
				tb.Errorf("event %s: %v", id, m.ContractErr)
			}
		}
	}
	return got
}

// Absent receives during window and fails tb if any of ids arrives. No
// receive is canceled halfway: a long poll canceled by the client stays open
// in the broker and would take, and hide for the visibility timeout, the
// event delivered right after the window (spec M5 §2.2).
func (a *Audit) Absent(tb testing.TB, window time.Duration, ids ...string) {
	tb.Helper()
	for deadline := time.Now().Add(window); time.Now().Before(deadline); {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(tb.Context()), receiveTimeout)
		err := a.receive(ctx)
		cancel()
		if err != nil {
			tb.Fatalf("audit: %v", err)
		}
	}
	for id, deliveries := range a.lookupAll(ids) {
		if len(deliveries) > 0 {
			tb.Errorf("event %s delivered %d time(s), want none", id, len(deliveries))
		}
	}
}

func (a *Audit) lookup(ids []string) (map[string][]AuditMessage, []string) {
	got := a.lookupAll(ids)
	var missing []string
	for _, id := range ids {
		if len(got[id]) == 0 {
			missing = append(missing, id)
		}
	}
	return got, missing
}

func (a *Audit) lookupAll(ids []string) map[string][]AuditMessage {
	a.mu.Lock()
	defer a.mu.Unlock()
	got := make(map[string][]AuditMessage, len(ids))
	for _, id := range ids {
		got[id] = slices.Clone(a.byID[id])
	}
	return got
}

// receive reads one batch (long poll of 1 s, hidden for auditVisibility),
// records it and deletes it.
func (a *Audit) receive(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	out, err := a.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl: aws.String(a.url), MaxNumberOfMessages: 10, WaitTimeSeconds: 1, VisibilityTimeout: auditVisibility,
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{
			types.MessageSystemAttributeNameMessageGroupId, types.MessageSystemAttributeNameMessageDeduplicationId,
		},
		MessageAttributeNames: []string{"All"},
	})
	if err != nil {
		return err
	}
	if len(out.Messages) == 0 {
		return nil
	}
	entries := make([]types.DeleteMessageBatchRequestEntry, 0, len(out.Messages))
	a.mu.Lock()
	for i, m := range out.Messages {
		a.record(m)
		entries = append(entries, types.DeleteMessageBatchRequestEntry{Id: aws.String(fmt.Sprint(i)), ReceiptHandle: m.ReceiptHandle})
	}
	a.mu.Unlock()
	_, err = a.client.DeleteMessageBatch(context.WithoutCancel(ctx), &sqs.DeleteMessageBatchInput{QueueUrl: aws.String(a.url), Entries: entries})
	return err
}

// record keeps one message, once; a.mu is held.
func (a *Audit) record(m types.Message) {
	if _, ok := a.seen[aws.ToString(m.MessageId)]; ok {
		return // delivered again by the broker: its delete did not arrive
	}
	a.seen[aws.ToString(m.MessageId)] = struct{}{}
	body := []byte(aws.ToString(m.Body))
	msg := AuditMessage{
		Body:        body,
		GroupID:     m.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)],
		DedupID:     m.Attributes[string(types.MessageSystemAttributeNameMessageDeduplicationId)],
		Attributes:  map[string]Attribute{},
		ContractErr: a.contract.Validate(body),
	}
	for name, v := range m.MessageAttributes {
		msg.Attributes[name] = Attribute{Type: aws.ToString(v.DataType), Value: aws.ToString(v.StringValue)}
	}
	var head struct {
		EventID string `json:"eventId"`
	}
	_ = json.Unmarshal(body, &head) // an unreadable body stays under ""
	a.byID[head.EventID] = append(a.byID[head.EventID], msg)
}
