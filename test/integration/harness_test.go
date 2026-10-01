//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	snstypes "github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/KaioVinicios/pda/api"
	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: HTTP-08, AUTH-08, DOC-06, D-20
//
// The in-process application answers through the contract validator.
func TestHarness(t *testing.T) {
	t.Parallel()
	anonymous := server.Client(t, "")

	ready := anonymous.Do(t, testkit.Request{Method: http.MethodGet, Path: "/health/ready"})
	var health struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	ready.JSON(t, &health)
	if ready.Status != http.StatusOK || health.Checks["postgres"] != "UP" || health.Checks["sqs"] != "UP" {
		t.Fatalf("GET /health/ready = %d %+v", ready.Status, health)
	}

	doc := anonymous.Do(t, testkit.Request{Method: http.MethodGet, Path: "/openapi.yaml"})
	if doc.Status != http.StatusOK || !bytes.Equal(doc.Body, api.OpenAPI) {
		t.Fatalf("GET /openapi.yaml = %d, %d bytes", doc.Status, len(doc.Body))
	}

	denied := anonymous.Do(t, testkit.Request{Method: http.MethodGet, Path: "/wallets/" + testkit.NewID()})
	if p := denied.Problem(t); denied.Status != http.StatusUnauthorized || p.Code != "UNAUTHENTICATED" {
		t.Fatalf("GET /wallets/{id} without a token = %d %s", denied.Status, p.Code)
	}
}

// auditEnvelope is a valid WalletBalanceChanged envelope; extra adds fields to data.
func auditEnvelope(eventID, walletID, extra string) string {
	return `{"eventId":"` + eventID + `","eventType":"WalletBalanceChanged","version":1,"aggregateType":"Wallet",` +
		`"aggregateId":"` + walletID + `","correlationId":"corr-audit","occurredAt":"2026-09-29T12:00:00.123Z","data":{` +
		`"walletId":"` + walletID + `","transactionId":"` + testkit.NewID() + `","transactionKind":"BET","direction":"DEBIT",` +
		`"money":{"amount":"25.00","currency":"BRL"},"balanceBefore":{"amount":"100.00","currency":"BRL"},` +
		`"balanceAfter":{"amount":"75.00","currency":"BRL"},"walletVersion":2` + extra + `}}`
}

// Covers: OUT-07 (harness do M4)
//
// The isolated topic delivers raw to its audit queue; the collector keeps
// every delivery per event id, in the order of the group, and checks each one
// against api/events.yaml.
func TestAuditCollector(t *testing.T) {
	t.Parallel()
	root := testkit.RootAWSConfig(t)
	snsClient, sqsClient := sns.NewFromConfig(root), sqs.NewFromConfig(root)
	topic := testkit.NewEventsTopic(t, sqsClient, snsClient)
	audit, err := testkit.NewAudit(t.Context(), sqsClient, topic.AuditQueueURL)
	if err != nil {
		t.Fatal(err)
	}
	wallet := testkit.NewID()
	publish := func(body, dedup string) {
		t.Helper()
		if _, err := snsClient.Publish(t.Context(), &sns.PublishInput{
			TopicArn: aws.String(topic.ARN), Message: aws.String(body),
			MessageGroupId: aws.String(wallet), MessageDeduplicationId: aws.String(dedup),
			MessageAttributes: map[string]snstypes.MessageAttributeValue{
				"eventType":    {DataType: aws.String("String"), StringValue: aws.String("WalletBalanceChanged")},
				"eventVersion": {DataType: aws.String("Number"), StringValue: aws.String("1")},
			},
		}); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	good, bad, last := testkit.NewID(), testkit.NewID(), testkit.NewID()
	publish(auditEnvelope(good, wallet, ""), good)
	publish(auditEnvelope(good, wallet, ""), good+"-again") // republished past the dedup window
	publish(auditEnvelope(bad, wallet, `,"note":"x"`), bad)
	publish(auditEnvelope(last, wallet, ""), last)

	ctx, cancel := context.WithTimeout(t.Context(), testkit.AuditTimeout)
	defer cancel()
	if _, err := audit.Wait(ctx, last); err != nil { // same group: the earlier ones arrived before it
		t.Fatalf("Wait: %v", err)
	}
	got, err := audit.Wait(ctx, good, bad)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if n := len(got[good]); n != 2 {
		t.Fatalf("deliveries of %s = %d, want 2", good, n)
	}
	first := got[good][0]
	switch {
	case first.ContractErr != nil:
		t.Fatalf("valid envelope flagged: %v", first.ContractErr)
	case first.GroupID != wallet || first.DedupID != good:
		t.Fatalf("group %q dedup %q, want %s %s", first.GroupID, first.DedupID, wallet, good)
	case first.Attributes["eventType"] != testkit.Attribute{Type: "String", Value: "WalletBalanceChanged"},
		first.Attributes["eventVersion"] != testkit.Attribute{Type: "Number", Value: "1"}:
		t.Fatalf("attributes = %v", first.Attributes)
	}
	if got[bad][0].ContractErr == nil {
		t.Fatal("envelope with a field outside the schema was not flagged")
	}
	audit.Absent(t, time.Second, testkit.NewID())
}

// Covers: OUT-10 (spec M5 §2.2, decision 18)
// Sensitivity: the Absent of M4, which canceled its last long poll at the end
// of the window → "event sent after Absent: … not delivered".
//
// An event that arrives right after Absent is still collected: Absent leaves
// no long poll canceled halfway, which the broker keeps open until its wait
// ends and which takes the next message, hiding it for a visibility timeout
// (the flake of I05b).
func TestAuditAbsentLeavesNoPollBehind(t *testing.T) {
	t.Parallel()
	root := testkit.RootAWSConfig(t)
	snsClient, sqsClient := sns.NewFromConfig(root), sqs.NewFromConfig(root)
	topic := testkit.NewEventsTopic(t, sqsClient, snsClient)
	audit, err := testkit.NewAudit(t.Context(), sqsClient, topic.AuditQueueURL)
	if err != nil {
		t.Fatal(err)
	}
	wallet := testkit.NewID()
	for range 3 {
		id := testkit.NewID()
		audit.Absent(t, 1500*time.Millisecond, id)
		// Straight into the audit queue: nothing else receives from it meanwhile.
		testkit.SendMessage(t, sqsClient, topic.AuditQueueURL, auditEnvelope(id, wallet, ""), testkit.SendOpts{GroupID: wallet, DedupID: id, NoToken: true})
		time.Sleep(300 * time.Millisecond)
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		_, err := audit.Wait(ctx, id)
		cancel()
		if err != nil {
			t.Fatalf("event sent after Absent: %v", err)
		}
	}
}

// onceHTTP sends every request to the SDK transport, except the first one of
// the SQS action target that intercept picks (hit): the broker's answer to that
// one is replaced, as a network fault would (spec audit-lost-receive, decision 5).
type onceHTTP struct {
	next      aws.HTTPClient
	target    string // X-Amz-Target, e.g. AmazonSQS.ReceiveMessage
	intercept func(next aws.HTTPClient, req *http.Request) (*http.Response, bool, error)
	hit       atomic.Bool
}

func (c *onceHTTP) Do(req *http.Request) (*http.Response, error) {
	if c.hit.Load() || req.Header.Get("X-Amz-Target") != c.target {
		return c.next.Do(req)
	}
	resp, hit, err := c.intercept(c.next, req)
	if hit {
		c.hit.Store(true)
	}
	return resp, err
}

// auditThrough is an Audit of a new isolated topic whose SQS client goes
// through c; the returned client reaches the broker directly.
func auditThrough(t *testing.T, c *onceHTTP) (*testkit.Audit, *sqs.Client, testkit.EventsTopic) {
	t.Helper()
	root := testkit.RootAWSConfig(t)
	sqsClient := sqs.NewFromConfig(root)
	topic := testkit.NewEventsTopic(t, sqsClient, sns.NewFromConfig(root))
	c.next = awshttp.NewBuildableClient()
	faulty := root.Copy()
	faulty.HTTPClient = c
	audit, err := testkit.NewAudit(t.Context(), sqs.NewFromConfig(faulty), topic.AuditQueueURL)
	if err != nil {
		t.Fatal(err)
	}
	return audit, sqsClient, topic
}

// Covers: TST-I05 (spec audit-lost-receive, decision 1)
//
// A receive the broker served but whose response never reached the Audit (the
// SDK retries a connection reset) hides its messages only for the Audit's own
// visibility, not for the queue's 30 s: every event of the group still arrives
// within AuditTimeout (the flake of I05a on CI).
func TestAuditRecoversLostReceive(t *testing.T) {
	t.Parallel()
	lose := &onceHTTP{target: "AmazonSQS.ReceiveMessage", intercept: func(next aws.HTTPClient, req *http.Request) (*http.Response, bool, error) {
		resp, err := next.Do(req)
		if err != nil {
			return resp, false, err
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, false, err
		}
		if !bytes.Contains(body, []byte(`"Messages"`)) {
			resp.Body = io.NopCloser(bytes.NewReader(body))
			return resp, false, nil
		}
		return nil, true, errors.New("read tcp: connection reset by peer")
	}}
	audit, sqsClient, topic := auditThrough(t, lose)
	wallet := testkit.NewID()
	ids := []string{testkit.NewID(), testkit.NewID(), testkit.NewID()}
	for _, id := range ids {
		testkit.SendMessage(t, sqsClient, topic.AuditQueueURL, auditEnvelope(id, wallet, ""), testkit.SendOpts{GroupID: wallet, DedupID: id, NoToken: true})
	}
	audit.WaitFor(t, ids...)
	if !lose.hit.Load() {
		t.Fatal("no receive response was lost: the test proves nothing")
	}
}

// Covers: TST-I05 (spec audit-lost-receive, decision 2)
//
// A message the broker delivers again, because its delete did not arrive, is
// kept once: only a new message from the topic counts as a new delivery.
func TestAuditCountsRedeliveryOnce(t *testing.T) {
	t.Parallel()
	swallow := &onceHTTP{target: "AmazonSQS.DeleteMessageBatch", intercept: func(_ aws.HTTPClient, req *http.Request) (*http.Response, bool, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Request: req,
			Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
			Body:   io.NopCloser(strings.NewReader(`{"Successful":[],"Failed":[]}`)),
		}, true, nil
	}}
	audit, sqsClient, topic := auditThrough(t, swallow)
	wallet, id, last := testkit.NewID(), testkit.NewID(), testkit.NewID()
	send := func(id string) {
		t.Helper()
		testkit.SendMessage(t, sqsClient, topic.AuditQueueURL, auditEnvelope(id, wallet, ""), testkit.SendOpts{GroupID: wallet, DedupID: id, NoToken: true})
	}
	send(id)
	audit.WaitFor(t, id)
	send(last)
	got := audit.WaitFor(t, last, id) // same group: last comes only with or after id's redelivery
	if !swallow.hit.Load() {
		t.Fatal("no delete was swallowed: the test proves nothing")
	}
	if n := len(got[id]); n != 1 {
		t.Fatalf("deliveries of %s = %d, want 1 (a redelivery counted as a new delivery)", id, n)
	}
}
