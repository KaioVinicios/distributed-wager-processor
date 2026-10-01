// Package sqsconsumer consumes wager-transactions.fifo (D-12, messaging.md §4).
package sqsconsumer

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/KaioVinicios/pda/internal/adapters/awsclient"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/faultinject"
)

// Processor concludes one message: app.ConsumeWager.
type Processor interface {
	Execute(ctx context.Context, m app.WagerMessage) (app.ConsumeResult, error)
}

// QueueAPI is the subset of *sqs.Client the consumer uses.
type QueueAPI interface {
	ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, optFns ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(ctx context.Context, in *sqs.DeleteMessageInput, optFns ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
	ChangeMessageVisibility(ctx context.Context, in *sqs.ChangeMessageVisibilityInput, optFns ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error)
	SendMessage(ctx context.Context, in *sqs.SendMessageInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
	GetQueueAttributes(ctx context.Context, in *sqs.GetQueueAttributesInput, optFns ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error)
}

// Metrics is what the consumer reports (messaging.md §8).
type Metrics interface {
	Received()
	Processed(outcome string, d time.Duration)
	Duplicate(layer string)
	Retried(reason string)
	SentToDLQ(reason string)
	DLQDepth(queue string, n int)
	ReceiveFailed()
	DeleteFailed()
	AuthFailure(reason string)
}

// Options are the consumer settings (messaging.md §4.1).
type Options struct {
	Pollers           int
	ReceiveBatch      int
	WaitTime          time.Duration
	Visibility        time.Duration
	ProcessingTimeout time.Duration
	MaxInFlight       int
	RetryMaxDelay     time.Duration
	ShutdownTimeout   time.Duration
	DLQName           string // label of sqs_dlq_depth
}

const (
	// receiveRetryMin and receiveRetryMax bound the wait after a failed
	// ReceiveMessage (messaging.md §4.3).
	receiveRetryMin = time.Second
	receiveRetryMax = 30 * time.Second
	// gateInterval is how often a paused consumer pings the database.
	gateInterval = 2 * time.Second
	// queueTimeout bounds each delete, visibility change and DLQ send, which
	// run detached from the cancellation of the work (messaging.md §4.5).
	queueTimeout = 2 * time.Second
	// emptyPollPause spaces the empty receives of short polling (SQS_WAIT_TIME=0).
	emptyPollPause = 100 * time.Millisecond
	// dlqDepthEvery is how often sqs_dlq_depth is refreshed.
	dlqDepthEvery = 30 * time.Second
	// correlationAttribute is the optional message attribute of messaging.md §3.2.
	correlationAttribute = "correlationId"
)

// Consumer is the SQS consumer of one instance (D-12). Any number of
// instances consume the same queue.
type Consumer struct {
	api     QueueAPI
	queues  *awsclient.Queues
	proc    Processor
	authn   Authenticator
	gate    *healthGate
	metrics Metrics
	log     *slog.Logger
	opts    Options
	sem     chan struct{}

	// stopPolling stops the receiving; abortWork cancels the processing in
	// flight, only when the shutdown deadline passes (messaging.md §4.5).
	stopPolling, abortWork context.CancelFunc
	stopping               atomic.Bool
	running                sync.WaitGroup
}

// NewConsumer builds a consumer; Start starts it.
func NewConsumer(api QueueAPI, queues *awsclient.Queues, proc Processor, authn Authenticator, db Pinger, m Metrics, log *slog.Logger, opts Options) *Consumer {
	return &Consumer{
		api: api, queues: queues, proc: proc, authn: authn, gate: newHealthGate(db, log, gateInterval), metrics: m, log: log,
		opts: opts, sem: make(chan struct{}, opts.MaxInFlight),
	}
}

// Start runs the pollers and the DLQ depth gauge until Stop, detached from the
// cancellation of ctx (the start of the application). poll carries the
// receiving and work the processing, so that the shutdown ends them apart.
func (c *Consumer) Start(ctx context.Context) {
	poll, stopPolling := context.WithCancel(context.WithoutCancel(ctx))
	work, abortWork := context.WithCancel(context.WithoutCancel(ctx))
	c.stopPolling, c.abortWork = stopPolling, abortWork
	for range c.opts.Pollers {
		c.running.Go(func() { c.pollLoop(poll, work) })
	}
	c.running.Go(func() { c.dlqDepthLoop(poll) })
}

// Stop is the shutdown of messaging.md §4.5: stop receiving; release what was
// received and not started; wait for the messages in flight until
// ShutdownTimeout (or ctx); then cancel them, which rolls their transactions
// back, and release them. No message is deleted without a commit.
func (c *Consumer) Stop(ctx context.Context) error {
	c.stopping.Store(true)
	c.stopPolling()
	ctx, cancel := context.WithTimeout(ctx, c.opts.ShutdownTimeout)
	defer cancel()
	done := make(chan struct{})
	go func() {
		c.running.Wait()
		close(done)
	}()
	select {
	case <-done:
		c.abortWork()
		return nil
	case <-ctx.Done():
	}
	c.log.Warn("sqs consumer: shutdown deadline reached, canceling the messages in flight")
	c.abortWork()
	select {
	case <-done:
		return nil
	case <-time.After(2 * queueTimeout):
		return ctx.Err()
	}
}

func (c *Consumer) pollLoop(poll, work context.Context) {
	var delay time.Duration
	for poll.Err() == nil {
		if err := c.gate.Wait(poll); err != nil {
			return
		}
		out, err := c.api.ReceiveMessage(poll, &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(c.queues.WagerURL),
			MaxNumberOfMessages: int32(c.opts.ReceiveBatch),             //nolint:gosec // 1..10, validated by config
			WaitTimeSeconds:     int32(c.opts.WaitTime / time.Second),   //nolint:gosec // 0..20, validated by config
			VisibilityTimeout:   int32(c.opts.Visibility / time.Second), //nolint:gosec // ≤ 12 h, validated by config
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{
				types.MessageSystemAttributeNameMessageGroupId, types.MessageSystemAttributeNameApproximateReceiveCount,
				types.MessageSystemAttributeNameSentTimestamp,
			},
			MessageAttributeNames: []string{correlationAttribute, AccessTokenAttribute},
		})
		if err != nil {
			if poll.Err() != nil {
				return
			}
			c.metrics.ReceiveFailed()
			delay = min(max(2*delay, receiveRetryMin), receiveRetryMax)
			c.log.Warn("sqs receive failed", "error", err.Error(), "retryIn", delay.String())
			select {
			case <-poll.Done():
				return
			case <-time.After(delay):
			}
			continue
		}
		delay = 0
		if len(out.Messages) == 0 && c.opts.WaitTime == 0 {
			// Short polling: an empty queue would otherwise be polled in a busy loop.
			select {
			case <-poll.Done():
				return
			case <-time.After(emptyPollPause):
			}
			continue
		}
		c.handleBatch(poll, work, out.Messages, time.Now())
	}
}

// handleBatch runs the groups of a batch in parallel and the messages of a
// group in order, and returns when all of them are settled.
func (c *Consumer) handleBatch(poll, work context.Context, msgs []types.Message, receivedAt time.Time) {
	for range msgs {
		c.metrics.Received()
	}
	var wg sync.WaitGroup
	for _, group := range groupBatch(msgs) {
		wg.Go(func() { c.handleGroup(poll, work, group, receivedAt) })
	}
	wg.Wait()
}

// handleGroup processes the group in order. A message that is retried or
// released holds the rest of its group, which is released too, so the order
// of the group survives (messaging.md §4.2). Before each message: the
// shutdown releases what was not started, and a message whose visibility may
// run out before its processing deadline is released unprocessed (spec M5,
// decision 8).
func (c *Consumer) handleGroup(poll, work context.Context, group []types.Message, receivedAt time.Time) {
	for i, msg := range group {
		select {
		case c.sem <- struct{}{}:
		case <-poll.Done():
			c.releaseAll(work, group[i:], "")
			return
		}
		if c.stopping.Load() {
			<-c.sem
			c.releaseAll(work, group[i:], "")
			return
		}
		if c.opts.Visibility-time.Since(receivedAt) < c.opts.ProcessingTimeout {
			<-c.sem
			c.releaseAll(work, group[i:], "deadline_release")
			return
		}
		a := c.handle(work, msg, receivedAt)
		<-c.sem
		if a.kind == actRetry || a.kind == actRelease {
			c.releaseAll(work, group[i+1:], "")
			return
		}
	}
}

// handle concludes one message and applies the action, in the order of D-23:
// the provider's token, the envelope, the provider of the body, then the use
// case. Nothing is read before the authorization.
func (c *Consumer) handle(work context.Context, msg types.Message, receivedAt time.Time) action {
	start := time.Now()
	log := c.log.With("sqsMessageId", aws.ToString(msg.MessageId))
	ctx, cancel := context.WithTimeout(work, c.opts.ProcessingTimeout)
	defer cancel()
	var (
		m   app.WagerMessage
		res app.ConsumeResult
	)
	p, err := authenticate(ctx, c.authn, msg, receivedAt)
	if err == nil {
		m, err = parseEnvelope(aws.ToString(msg.Body), aws.ToString(msg.MessageAttributes[correlationAttribute].StringValue), receivedAt)
	}
	if err == nil {
		log = log.With(messageIDs(m)...)
		err = matchProvider(p, m)
	}
	if err == nil {
		res, err = c.proc.Execute(ctx, m)
	}
	c.countRefusal(err)
	a := decide(conclude(res, err), c.stopping.Load(), receiveCount(msg), c.opts.RetryMaxDelay)
	c.apply(work, msg, a, err, log)
	if a.outcome != "" {
		c.metrics.Processed(a.outcome, time.Since(start))
	}
	if a.duplicate != "" {
		c.metrics.Duplicate(a.duplicate)
	}
	return a
}

// messageIDs are the identifiers a parsed message gives to the logs of its
// handling (OBS-01): the envelope's messageId and correlationId, and the
// wallet and provider it names. Those two are still unvalidated input, so
// they are cut to maxMessageIDLen characters; slog's JSON handler escapes them.
func messageIDs(m app.WagerMessage) []any {
	ids := []any{"messageId", m.MessageID, "correlationId", m.CorrelationID}
	if v := m.Input.WalletID; v != nil {
		ids = append(ids, "walletId", cutRunes(*v, maxMessageIDLen))
	}
	if v := m.Input.ProviderID; v != nil {
		ids = append(ids, "providerId", cutRunes(*v, maxMessageIDLen))
	}
	return ids
}

// cutRunes keeps at most n characters of s.
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// apply performs the action on the queue, detached from the cancellation of
// the work, so a shutdown never leaves a concluded message undeleted.
func (c *Consumer) apply(work context.Context, msg types.Message, a action, cause error, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(work), queueTimeout)
	defer cancel()
	switch a.kind {
	case actDelete:
		faultinject.Point("consumer.after_commit_before_delete") // crash between the commit and the delete (test-plan §4)
		c.delete(ctx, msg, log)
	case actDLQ:
		if _, err := c.api.SendMessage(ctx, dlqInput(c.queues.DLQURL, msg, a, time.Now())); err != nil {
			delay := retryDelay(receiveCount(msg), c.opts.RetryMaxDelay)
			log.Warn("sqs dlq send failed", "errorCode", a.code, "error", err.Error())
			c.changeVisibility(ctx, msg, delay, log)
			c.metrics.Retried("transient")
			return
		}
		c.metrics.SentToDLQ(a.code)
		log.Warn("sqs message sent to the dlq", "errorCode", a.code, "errorCategory", a.category)
		c.delete(ctx, msg, log)
	case actRetry:
		log.Warn("sqs message failed transiently", "retryIn", a.delay.String(), "error", errorText(cause))
		c.changeVisibility(ctx, msg, a.delay, log)
		c.metrics.Retried("transient")
		if a.transient {
			c.gate.Report(ctx)
		}
	case actRelease:
		c.changeVisibility(ctx, msg, 0, log)
	}
}

// releaseAll makes msgs visible again at once; reason, when set, is counted.
func (c *Consumer) releaseAll(work context.Context, msgs []types.Message, reason string) {
	if len(msgs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(work), queueTimeout)
	defer cancel()
	for _, msg := range msgs {
		c.changeVisibility(ctx, msg, 0, c.log.With("sqsMessageId", aws.ToString(msg.MessageId)))
		if reason != "" {
			c.metrics.Retried(reason)
		}
	}
}

func (c *Consumer) delete(ctx context.Context, msg types.Message, log *slog.Logger) {
	if _, err := c.api.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl: aws.String(c.queues.WagerURL), ReceiptHandle: msg.ReceiptHandle,
	}); err != nil {
		c.metrics.DeleteFailed()
		log.Warn("sqs delete failed", "error", err.Error())
	}
}

func (c *Consumer) changeVisibility(ctx context.Context, msg types.Message, d time.Duration, log *slog.Logger) {
	if _, err := c.api.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl: aws.String(c.queues.WagerURL), ReceiptHandle: msg.ReceiptHandle,
		VisibilityTimeout: int32(d / time.Second), //nolint:gosec // ≤ 12 h, validated by config
	}); err != nil {
		log.Warn("sqs visibility change failed", "error", err.Error())
	}
}

// dlqDepthLoop refreshes sqs_dlq_depth, which also counts the redrives.
func (c *Consumer) dlqDepthLoop(poll context.Context) {
	tick := time.NewTicker(dlqDepthEvery)
	defer tick.Stop()
	for {
		out, err := c.api.GetQueueAttributes(poll, &sqs.GetQueueAttributesInput{
			QueueUrl:       aws.String(c.queues.DLQURL),
			AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages},
		})
		if err == nil {
			if n, err := strconv.Atoi(out.Attributes[string(types.QueueAttributeNameApproximateNumberOfMessages)]); err == nil {
				c.metrics.DLQDepth(c.opts.DLQName, n)
			}
		}
		select {
		case <-poll.Done():
			return
		case <-tick.C:
		}
	}
}

// receiveCount is the ApproximateReceiveCount of msg, 1 when unknown.
func receiveCount(msg types.Message) int {
	n, err := strconv.Atoi(msg.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)])
	if err != nil {
		return 1
	}
	return n
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
