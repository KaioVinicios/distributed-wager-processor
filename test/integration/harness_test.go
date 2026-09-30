//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
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
