//go:build e2e

package e2e_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/KaioVinicios/pda/internal/config"
	"github.com/KaioVinicios/pda/test/testkit"
)

// The tests of this file pause a service of the compose or stop an instance:
// none of them runs in parallel (spec M9, decision 2).

// sqsOutage is how long R02 keeps the MiniStack paused (test-plan §5.5).
const sqsOutage = 10 * time.Second

// Covers: F6, OUT-04, HTTP-08, OBS-04 (R02)
// Sensitivity: the SQS checker always nil → the instances stay ready during the outage; the retry of a
// failed Publish scheduled 1 h ahead (no cap) → the outbox never empties. Not detected, by design: the
// failure path marking the event published, because a Publish that timed out on a paused broker is
// still delivered once the broker resumes (spec M9, execution finding).
//
// The broker is paused: the instances report unready, the HTTP goes on, the
// outbox piles up and ages, and after the broker returns every event is
// published and reaches the audit queue (item 8 of the check of each wallet).
func TestSQSOutage(t *testing.T) {
	defer cluster.Restore(t)
	wallets := make([]testkit.Wallet, 3)
	ids := make([]string, 3)
	for i := range wallets {
		wallets[i] = cluster.OpenWallet(t, testkit.BRL("100.00"))
		ids[i] = wallets[i].ID
	}
	a := cluster.Client(t, "provider-a")

	resume := testkit.Pause(t, "ministack")
	pausedAt := time.Now()
	waitReady(t, http.StatusServiceUnavailable)
	for i := range 6 {
		bet := wager(wallets[i%3], "provider-a", "BET", "1.00", unique("bet"), "")
		if r := result(t, a, bet, http.StatusOK); r.Status != "PROCESSED" {
			t.Fatalf("BET during the outage = %+v, want PROCESSED", r)
		}
	}
	within(t, sqsOutage, "outbox backlog reported", func() bool {
		var pending int64
		for i := range 3 {
			pending += cluster.Instance(i).MetricValue(t, "outbox_pending_events")
		}
		return pending > 0
	})
	testkit.Eventually(t, sqsOutage, "oldest pending event aging", func(ctx context.Context) (bool, error) {
		var age int
		err := cluster.Owner().QueryRow(ctx, `SELECT COALESCE(EXTRACT(EPOCH FROM now() - min(occurred_at))::int, 0)
			FROM outbox_events WHERE published_at IS NULL AND message_group_id = ANY($1)`, ids).Scan(&age)
		return age >= 3, err
	})
	time.Sleep(time.Until(pausedAt.Add(sqsOutage))) // the length of the outage, not a synchronization
	resume()

	waitReady(t, http.StatusOK)
	for _, w := range wallets {
		waitPublished(t, w.ID)
	}
}

// pgOutage is how long R01 keeps the PostgreSQL paused: the first failure of
// the consumer (3 s of deadline + 1 s of ping) and then more than one
// redelivery cycle (3 s + 2 s of backoff) with the consumer paused (spec M9,
// decision 3).
const pgOutage = 15 * time.Second

// consumerOnlyOn0 leaves the SQS consumer on instance 0 alone, with extra on
// it when not nil; instances 1 and 2 run every other role. It returns once
// the long polls of the consumers it stopped are over, so no message the test
// sends next is hidden by one (spec r03-orphan-poll). The test defers
// cluster.Restore.
func consumerOnlyOn0(t *testing.T, extra map[string]string) {
	t.Helper()
	others := testkit.EnvOf(config.Roles{HTTP: true, OutboxPublisher: true, ReferenceWorker: true})
	cluster.Restart(t, 1, others)
	cluster.Restart(t, 2, others)
	if extra != nil {
		cluster.Restart(t, 0, extra)
	}
	cluster.AwaitOrphanPolls()
}

// httpCall is one BET of R01 and what came back.
type httpCall struct {
	bet            testkit.Wager
	at             time.Time
	status         int
	retryAfter     string
	code           string
	transportError error
}

// Covers: F6, SQS-07, HTTP-08, OBS-04, E5 (R01)
// Sensitivity: withDeadline returning next → no HTTP answer during the outage (the red of the finding);
// healthGate.Report a no-op → the consumer never pauses; healthGate.Wait always nil → the paused
// consumer received 30 messages during the outage.
//
// The database is paused for 15 s under HTTP (3 instances) and SQS traffic.
// While it is down the HTTP answers 503 with Retry-After within
// HTTP_REQUEST_TIMEOUT, every instance reports unready and the consumer,
// alone on instance 0, stops receiving once paused. Afterwards every message
// is processed once, none reaches the DLQ, and every BET answered with 503
// is sent again with its key and recorded once.
func TestPostgresOutage(t *testing.T) {
	defer cluster.Restore(t)
	consumerOnlyOn0(t, nil)
	wallets := make([]testkit.Wallet, 5)
	for i := range wallets {
		wallets[i] = cluster.OpenWallet(t, testkit.BRL("1000.00"))
	}
	a := cluster.Client(t, "provider-a")
	consumer := cluster.Instance(0)

	var (
		mu        sync.Mutex
		calls     []httpCall
		msgs      []string
		perWallet = map[string]int{}
		inFlight  sync.WaitGroup
	)
	stop := make(chan struct{})
	generated := make(chan struct{})
	// Stops the traffic and waits for it, also when the test fails midway:
	// nothing may call t after the test ended.
	stopTraffic := sync.OnceFunc(func() {
		close(stop)
		<-generated
		inFlight.Wait()
	})
	defer stopTraffic()
	go func() { // one BET by HTTP and one by SQS every 200 ms until stop
		defer close(generated)
		tick := time.NewTicker(200 * time.Millisecond)
		defer tick.Stop()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			w := wallets[i%len(wallets)]
			bet := wager(w, "provider-a", "BET", "1.00", unique("bet"), "")
			inFlight.Go(func() {
				c := httpCall{bet: bet}
				resp, err := a.Try(t, wagerRequest(bet))
				c.at, c.transportError = time.Now(), err
				if err == nil {
					c.status, c.retryAfter = resp.Status, resp.Header.Get("Retry-After")
					if resp.Status == http.StatusServiceUnavailable {
						c.code = resp.Problem(t).Code
					}
				}
				mu.Lock()
				calls = append(calls, c)
				mu.Unlock()
			})
			msgID := unique("msg")
			cluster.SendWager(t, sqsWager(t, msgID, wager(w, "provider-a", "BET", "1.00", unique("sqs"), "")), testkit.SendOpts{GroupID: w.ID})
			mu.Lock()
			msgs = append(msgs, msgID)
			perWallet[w.ID] += 2
			mu.Unlock()
		}
	}()

	time.Sleep(time.Second) // traffic before the outage, not a synchronization
	resume := testkit.Pause(t, "postgres")
	pausedAt := time.Now()
	waitReady(t, http.StatusServiceUnavailable)
	within(t, pgOutage, "consumer paused", func() bool {
		return strings.Contains(consumer.Logs(), "sqs consumer paused")
	})
	time.Sleep(3 * time.Second) // a receive already under way when the gate closed ends in this time
	if time.Until(pausedAt.Add(pgOutage)) < 2*time.Second {
		t.Fatalf("the consumer paused %v after the outage began: no window left to watch it", time.Since(pausedAt))
	}
	receivedPaused := consumer.MetricValue(t, "sqs_messages_received_total")
	time.Sleep(time.Until(pausedAt.Add(pgOutage))) // the length of the outage, not a synchronization
	if got := consumer.MetricValue(t, "sqs_messages_received_total"); got != receivedPaused {
		t.Fatalf("the paused consumer received %d messages during the outage, want none", got-receivedPaused)
	}
	resume()
	resumedAt := time.Now()
	time.Sleep(time.Second) // traffic after the outage, not a synchronization
	stopTraffic()

	var during int
	for _, c := range calls {
		if c.at.Before(pausedAt.Add(time.Second)) || c.at.After(resumedAt) {
			continue
		}
		during++
		if c.status != http.StatusServiceUnavailable || c.retryAfter != "1" || c.code != "TEMPORARILY_UNAVAILABLE" {
			t.Errorf("answer during the outage = %d %q Retry-After %q (transport error %v), want 503 TEMPORARILY_UNAVAILABLE with Retry-After 1",
				c.status, c.code, c.retryAfter, c.transportError)
		}
	}
	if during == 0 {
		t.Fatal("no HTTP answer arrived during the outage: the requests waited for the database")
	}

	waitReady(t, http.StatusOK)
	for _, c := range calls {
		if c.status != http.StatusOK {
			if r := result(t, a, c.bet, http.StatusOK); r.Status != "PROCESSED" {
				t.Fatalf("resend of %s = %+v, want PROCESSED", c.bet.ExternalTransactionID, r)
			}
		}
	}
	if outcomes := inboxOutcomes(t, msgs...); len(outcomes) != 1 || outcomes["PROCESSED"] != len(msgs) {
		t.Fatalf("inbox outcomes = %v, want %d PROCESSED and nothing else", outcomes, len(msgs))
	}
	cluster.AssertQueueDrained(t)
	if n := cluster.DLQDepth(t); n != 0 {
		t.Fatalf("DLQ depth = %d after the outage, want 0", n)
	}
	for _, w := range wallets {
		n := perWallet[w.ID]
		got := balanceOf(t, w.ID)
		if got.Balance != testkit.BRL(fmt.Sprintf("%d.00", 1000-n)) || got.Version != int64(1+n) {
			t.Fatalf("wallet %s = %+v, want %d debits of 1.00", w.ID, got, n)
		}
	}
}

// waitReady waits until GET /health/ready of every instance answers status.
func waitReady(t *testing.T, status int) {
	t.Helper()
	within(t, 30*time.Second, "every instance ready with "+http.StatusText(status), func() bool {
		for i := range 3 {
			if cluster.Instance(i).ReadyStatus(t) != status {
				return false
			}
		}
		return true
	})
}

// within polls cond every 100 ms until it holds, and fails t after d. Unlike
// testkit.Eventually it gives cond no context: the conditions of this file
// call helpers that take t (contextcheck).
func within(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(d); !cond(); time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%s: not reached within %v", what, d)
		}
	}
}

// stoppedInstance is the environment of the instance R03 and R04 stop: a
// lock timeout wider than the time the test takes to release its barrier,
// and room in the pool for every session waiting (spec M9, decision 4).
var stoppedInstance = map[string]string{"DB_LOCK_TIMEOUT": "4s", "DB_MAX_CONNS": "8"}

// lockWallets holds the row lock of the wallets in a transaction of the
// owner until release, which also runs in Cleanup.
func lockWallets(t *testing.T, ids ...string) (release func()) {
	t.Helper()
	tx, err := cluster.Owner().Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `SELECT 1 FROM wallets WHERE id = ANY($1) FOR UPDATE`, ids); err != nil {
		_ = tx.Rollback(context.WithoutCancel(t.Context()))
		t.Fatal(err)
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			if err := tx.Rollback(context.WithoutCancel(t.Context())); err != nil {
				t.Error(err)
			}
		})
	}
	t.Cleanup(release)
	return release
}

// logIndex is the position of the first log line that has every part, or -1.
func logIndex(logs string, parts ...string) int {
	pos := 0
	for line := range strings.Lines(logs) {
		match := true
		for _, p := range parts {
			if !strings.Contains(line, p) {
				match = false
				break
			}
		}
		if match {
			return pos
		}
		pos += len(line)
	}
	return -1
}

// waitLogLine waits until the current run of the instance logs a line with every part.
func waitLogLine(t *testing.T, n *testkit.Instance, parts ...string) {
	t.Helper()
	within(t, 5*time.Second, "log line with "+strings.Join(parts, " "), func() bool {
		return logIndex(n.Logs(), parts...) >= 0
	})
}

// inboxNow counts the inbox rows of the messages by outcome, without waiting
// for the rest (inboxOutcomes waits until every message has one).
func inboxNow(t *testing.T, messageIDs ...string) map[string]int {
	t.Helper()
	rows, err := cluster.Owner().Query(t.Context(), `SELECT outcome FROM inbox_messages WHERE message_id = ANY($1)`, messageIDs)
	if err != nil {
		t.Fatal(err)
	}
	outcomes, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, o := range outcomes {
		counts[o]++
	}
	return counts
}

// Covers: SQS-09, FX-04, F4 (R03)
// Sensitivity: Stop canceling the work at once → the inbox is empty when instance 0 is gone; releaseAll
// deleting instead of releasing → the messages received and not started are lost.
//
// The consumer, alone on instance 0, has one message of each of 3 wallets
// stuck on their locks and the rest received or queued when SIGTERM arrives.
// The instance stops receiving, waits for the 3 in flight (released by the
// test after the stop began), releases the rest and exits with 0 within
// SHUTDOWN_TIMEOUT, in the order HTTP → consumer → pool. The consumer of
// instance 1 then processes the other 27: every message exactly once.
func TestGracefulShutdownSQS(t *testing.T) {
	defer cluster.Restore(t)
	wallets := make([]testkit.Wallet, 3)
	ids := make([]string, 3)
	for i := range wallets {
		wallets[i] = cluster.OpenWallet(t, testkit.BRL("100.00"))
		ids[i] = wallets[i].ID
	}
	extra := maps.Clone(stoppedInstance)
	extra["SQS_PROCESSING_TIMEOUT"] = "4s"
	consumerOnlyOn0(t, extra)
	release := lockWallets(t, ids...)
	// Interleaved (A, B, C, A, …): the first receive then holds the three
	// groups, and each has its head in flight at once.
	var msgs []string
	for range 10 {
		for _, w := range wallets {
			id := unique("msg")
			cluster.SendWager(t, sqsWager(t, id, wager(w, "provider-a", "BET", "1.00", unique("bet"), "")), testkit.SendOpts{GroupID: w.ID})
			msgs = append(msgs, id)
		}
	}
	waitLockWaiters(t, 3)

	start := time.Now()
	stopped := cluster.StopAsync(0)
	waitLogLine(t, cluster.Instance(0), `"msg":"sqs consumer stopping"`)
	release()
	if err := <-stopped; err != nil {
		t.Fatalf("instance 0: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= 5*time.Second {
		t.Fatalf("instance 0 stopped in %v, want less than SHUTDOWN_TIMEOUT (5s)", elapsed)
	}
	logs := cluster.Instance(0).Logs()
	httpStop := logIndex(logs, `"msg":"http server stopping"`, `"server":"api"`)
	consumerStop := logIndex(logs, `"msg":"sqs consumer stopped"`)
	poolClosed := logIndex(logs, `"msg":"postgres pool closed"`)
	if httpStop < 0 || consumerStop <= httpStop || poolClosed <= consumerStop {
		t.Fatalf("stop order HTTP %d → consumer %d → pool %d, want increasing:\n%s", httpStop, consumerStop, poolClosed, logs)
	}
	if got := inboxNow(t, msgs...); len(got) != 1 || got["PROCESSED"] != 3 {
		t.Fatalf("inbox when instance 0 is gone = %v, want the 3 in flight PROCESSED and nothing else", got)
	}

	cluster.Restart(t, 1) // the consumer again, on another instance
	if got := inboxOutcomes(t, msgs...); len(got) != 1 || got["PROCESSED"] != len(msgs) {
		t.Fatalf("inbox outcomes = %v, want %d PROCESSED and nothing else", got, len(msgs))
	}
	cluster.AssertQueueDrained(t)
	if n := cluster.DLQDepth(t); n != 0 {
		t.Fatalf("DLQ depth = %d, want 0", n)
	}
	for _, w := range wallets {
		if got := balanceOf(t, w.ID); got.Balance != testkit.BRL("90.00") || got.Version != 11 {
			t.Fatalf("wallet %s = %+v, want 10 debits", w.ID, got)
		}
	}
}

// Covers: FX-04 (R04)
// Sensitivity: srv.Close instead of srv.Shutdown → the requests in flight end with EOF, without an answer.
//
// Five BETs to instance 0 wait on the lock of their wallet when SIGTERM
// arrives. Once the API server is stopping, a new connection is refused and
// its operation never exists; the five, released by the test, answer 200;
// the instance exits with 0 within SHUTDOWN_TIMEOUT.
func TestGracefulShutdownHTTP(t *testing.T) {
	defer cluster.Restore(t)
	w := cluster.OpenWallet(t, testkit.BRL("100.00"))
	cluster.Restart(t, 0, stoppedInstance)
	c := cluster.Instance(0).Client(t, "provider-a")
	release := lockWallets(t, w.ID)
	type answer struct {
		resp *testkit.Response
		err  error
	}
	answers := make(chan answer, 5)
	for range 5 {
		bet := wager(w, "provider-a", "BET", "1.00", unique("bet"), "")
		go func() {
			resp, err := c.Try(t, wagerRequest(bet))
			answers <- answer{resp, err}
		}()
	}
	waitLockWaiters(t, 5)

	start := time.Now()
	stopped := cluster.StopAsync(0)
	waitLogLine(t, cluster.Instance(0), `"msg":"http server stopping"`, `"server":"api"`)
	cluster.CloseIdleConnections()
	late := wager(w, "provider-a", "BET", "1.00", unique("late"), "")
	if resp, err := c.Try(t, wagerRequest(late)); !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("new request while stopping = %v %v, want connection refused", resp, err)
	}
	release()
	for range 5 {
		a := <-answers
		if a.err != nil || a.resp.Status != http.StatusOK {
			t.Fatalf("request in flight = %v %v, want 200", a.resp, a.err)
		}
	}
	if err := <-stopped; err != nil {
		t.Fatalf("instance 0: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= 5*time.Second {
		t.Fatalf("instance 0 stopped in %v, want less than SHUTDOWN_TIMEOUT (5s)", elapsed)
	}
	if got := balanceOf(t, w.ID); got.Balance != testkit.BRL("95.00") || got.Version != 6 {
		t.Fatalf("wallet = %+v, want the 5 debits in flight", got)
	}
	resp := cluster.Client(t, "provider-a").Do(t, testkit.Request{
		Method: http.MethodGet,
		Path:   "/providers/provider-a/wagering/transactions/" + late.ExternalTransactionID,
	})
	if resp.Status != http.StatusNotFound {
		t.Fatalf("the refused operation = %d, want 404: it was never recorded", resp.Status)
	}
}
