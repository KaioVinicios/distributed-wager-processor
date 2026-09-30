package testkit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/config"
)

const (
	// readyTimeout bounds the start of one process built with -race.
	readyTimeout = time.Minute
	// stopTimeout bounds a graceful stop (SHUTDOWN_TIMEOUT is 5 s in the tests).
	stopTimeout = 20 * time.Second
	// exitTimeout bounds the wait for a process to reach its fault point.
	exitTimeout = 30 * time.Second
	// killedCode is the exit code os/exec reports for a process ended by a signal.
	killedCode = -1
	// faultCode is the exit code of a process stopped at a fault point.
	faultCode = 137
)

// Cluster is n processes of the binary over the database, queues and topic of
// its Harness (test-plan §3.4). Every instance keeps HTTP on, so readiness is
// always GET /health/ready (spec M8, decision 9).
type Cluster struct {
	*Harness
	bin   string            // built with -tags faultinject -race
	dir   string            // the logs of every run; removed by Close unless PDA_TEST_KEEP=1
	base  map[string]string // the environment of every instance
	addrs [][2]string       // HTTP and admin listen address of each instance
	procs []*proc

	closeOnce sync.Once
}

// proc is one run of an instance. done is closed by the watcher once the
// process exited, after code is set.
type proc struct {
	target *target
	cmd    *exec.Cmd
	log    *syncBuffer // this run only
	file   *os.File    // every run of the instance, for the race check
	custom bool        // started with an override: Restore restarts it
	done   chan struct{}
	code   int
}

func (p *proc) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// StartCluster builds the binary once, starts n processes on free ports with
// the E2E times of test-plan §3.3 and waits for each to be ready. stop stops
// them (Close) and deletes the queues and the topic; it must run before the
// database cleanup (spec M8, decision 11).
func (e *Env) StartCluster(ctx context.Context, n int, opts ...func(*config.Config)) (*Cluster, func() error, error) {
	dir, err := os.MkdirTemp("", "pda-cluster-")
	if err != nil {
		return nil, nil, err
	}
	bin := filepath.Join(dir, "pda")
	if err := buildBinary(ctx, bin); err != nil {
		_ = os.RemoveAll(dir)
		return nil, nil, err
	}
	f, err := e.newFixture(ctx)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, nil, err
	}
	c := &Cluster{Harness: f.harness, bin: bin, dir: dir, procs: make([]*proc, n)}
	stop := func() error {
		err := c.Close()
		f.remove()
		return err
	}
	for range n {
		httpAddr, err := freeAddr(ctx)
		if err != nil {
			return nil, nil, errors.Join(err, stop())
		}
		metricsAddr, err := freeAddr(ctx)
		if err != nil {
			return nil, nil, errors.Join(err, stop())
		}
		c.addrs = append(c.addrs, [2]string{httpAddr, metricsAddr})
		c.targets = append(c.targets, newTarget(httpAddr, metricsAddr))
	}
	cfg := f.cfg
	cfg.LogLevel = "info"
	e2eTimes(&cfg)
	for _, opt := range opts {
		opt(&cfg)
	}
	cfg.HTTPAddr, cfg.MetricsAddr = c.addrs[0][0], c.addrs[0][1]
	if err := cfg.Validate(); err != nil {
		return nil, nil, errors.Join(fmt.Errorf("testkit: cluster config: %w", err), stop())
	}
	c.base = EnvOf(cfg)
	maps.Copy(c.base, EnvOf(config.Roles{HTTP: true, Consumer: true, OutboxPublisher: true, ReferenceWorker: true}))
	maps.Copy(c.base, map[string]string{
		"AWS_ENDPOINT_URL": MiniStackURL, "AWS_REGION": f.region,
		"AWS_ACCESS_KEY_ID": rootKey, "AWS_SECRET_ACCESS_KEY": rootKey,
		"HOME": dir, // nothing of the developer's ~/.aws reaches the instances
	})
	for i := range n {
		if err := c.start(ctx, i); err != nil {
			return nil, nil, errors.Join(err, stop())
		}
	}
	return c, stop, nil
}

// e2eTimes are the E2E column of test-plan §3.3 (spec M8, decision 7).
func e2eTimes(c *config.Config) {
	c.SQSWaitTime, c.SQSRetryMaxDelay = 2*time.Second, 2*time.Second
	c.OutboxLease, c.OutboxPollInterval = 3*time.Second, 200*time.Millisecond
	c.OutboxRetryBaseDelay, c.OutboxRetryMaxDelay = 200*time.Millisecond, 2*time.Second
	c.ReferenceRetryBaseDelay, c.ReferencePollInterval = 200*time.Millisecond, 100*time.Millisecond
	c.ReferenceMaxAttempts, c.ReferenceTTL = 3, 5*time.Second
}

// buildBinary compiles cmd/pda with the fault points and the race detector
// (test-plan §3.4, TST-C12).
func buildBinary(ctx context.Context, out string) error {
	root, err := findRepoRoot()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "go", "build", "-tags", "faultinject", "-race", "-o", out, "./cmd/pda")
	cmd.Dir = root
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("testkit: build the binary: %w\n%s", err, b)
	}
	return nil
}

// start runs instance i with the base environment plus the overrides and waits
// until it is ready. Any override marks the run as custom; one with PDA_FAULT
// arms the instance, which leaves the round-robin (spec M8, decision 15). The
// environment is built from scratch: nothing of the test process is inherited.
func (c *Cluster) start(ctx context.Context, i int, overrides ...map[string]string) error {
	env := maps.Clone(c.base)
	env["HTTP_ADDR"], env["METRICS_ADDR"] = c.addrs[i][0], c.addrs[i][1]
	for _, o := range overrides {
		maps.Copy(env, o)
	}
	file, err := os.OpenFile(filepath.Join(c.dir, fmt.Sprintf("instance-%d.log", i)),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	p := &proc{target: c.targets[i], log: &syncBuffer{}, file: file, custom: len(overrides) > 0, done: make(chan struct{})}
	// Not bound to ctx: the cluster decides when an instance ends.
	p.cmd = exec.CommandContext(context.WithoutCancel(ctx), c.bin) //nolint:gosec // the binary this cluster built
	p.cmd.Env = envList(env)
	p.cmd.Stdout = io.MultiWriter(p.log, file)
	p.cmd.Stderr = p.cmd.Stdout
	if err := p.cmd.Start(); err != nil {
		_ = file.Close()
		return fmt.Errorf("testkit: start instance %d: %w", i, err)
	}
	p.target.armed.Store(env["PDA_FAULT"] != "")
	p.target.live.Store(true)
	c.procs[i] = p
	go c.watch(p)
	return c.waitReady(ctx, i, p)
}

func envList(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for _, k := range slices.Sorted(maps.Keys(env)) {
		out = append(out, k+"="+env[k])
	}
	return out
}

// watch waits for the process, records its exit code and takes it out of the
// round-robin. The idle connections are dropped, so no request reuses one to
// a dead process after a restart on the same port.
func (c *Cluster) watch(p *proc) {
	err := p.cmd.Wait()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		p.code = exit.ExitCode() // -1 when a signal ended it
	case err != nil:
		p.code = killedCode
	}
	p.target.live.Store(false)
	c.http.CloseIdleConnections()
	_ = p.file.Close()
	close(p.done)
}

// ready tells whether the instance answers GET /health/ready with 200.
func (c *Cluster) ready(ctx context.Context, tg *target) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tg.baseURL+"/health/ready", nil)
	if err != nil {
		return false
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// waitReady polls the instance until it is ready, it exits, or readyTimeout.
func (c *Cluster) waitReady(ctx context.Context, i int, p *proc) error {
	ctx, cancel := context.WithTimeout(ctx, readyTimeout)
	defer cancel()
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	for !c.ready(ctx, p.target) {
		select {
		case <-p.done:
			return fmt.Errorf("testkit: instance %d exited with %d before it was ready:\n%s", i, p.code, p.log)
		case <-ctx.Done():
			return fmt.Errorf("testkit: instance %d not ready within %v:\n%s", i, readyTimeout, p.log)
		case <-tick.C:
		}
	}
	return nil
}

// terminate stops a live process with SIGTERM and waits for it; an exit code
// other than 0 is an error (66 is the race detector's). A process that already
// exited is an error only if it was neither killed nor stopped at a fault point.
func (c *Cluster) terminate(p *proc) error {
	if p == nil {
		return nil
	}
	if p.exited() {
		if p.code != 0 && p.code != killedCode && p.code != faultCode {
			return fmt.Errorf("exited by itself with code %d:\n%s", p.code, p.log)
		}
		return nil
	}
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	select {
	case <-p.done:
	case <-time.After(stopTimeout):
		_ = p.cmd.Process.Kill()
		<-p.done
		return fmt.Errorf("not stopped within %v:\n%s", stopTimeout, p.log)
	}
	if p.code != 0 {
		return fmt.Errorf("stopped with exit code %d:\n%s", p.code, p.log)
	}
	return nil
}

// Close stops every instance (spec M8, decision 11) and reports what went
// wrong in any of them: an unexpected exit, or a data race the race detector
// logged in any run (TST-C12). It must run before the database is dropped,
// and it runs once.
func (c *Cluster) Close() error {
	var err error
	c.closeOnce.Do(func() {
		var errs []error
		for i, p := range c.procs {
			if e := c.terminate(p); e != nil {
				errs = append(errs, fmt.Errorf("instance %d: %w", i, e))
			}
		}
		errs = append(errs, c.races()...)
		if os.Getenv("PDA_TEST_KEEP") != "1" {
			_ = os.RemoveAll(c.dir)
		}
		err = errors.Join(errs...)
	})
	return err
}

// races lists the instances whose log, over every run, has a data race.
func (c *Cluster) races() []error {
	var errs []error
	for i := range c.procs {
		raw, err := os.ReadFile(filepath.Join(c.dir, fmt.Sprintf("instance-%d.log", i)))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
		if strings.Contains(string(raw), "WARNING: DATA RACE") {
			errs = append(errs, fmt.Errorf("instance %d logged a data race (rerun with PDA_TEST_KEEP=1 to keep %s)", i, c.dir))
		}
	}
	return errs
}

// logRuns writes the log of the current run of every instance to tb.
func (c *Cluster) logRuns(tb testing.TB) {
	tb.Helper()
	for i, p := range c.procs {
		state := "running"
		if p.exited() {
			state = fmt.Sprintf("exited with %d", p.code)
		}
		tb.Logf("--- instance %d (%s) ---\n%s", i, state, p.log)
	}
}

// AttachLogs writes the log of the current run of every instance to tb's
// output if the test fails (test-plan §3.4).
func (c *Cluster) AttachLogs(tb testing.TB) {
	tb.Helper()
	tb.Cleanup(func() {
		if tb.Failed() {
			c.logRuns(tb)
		}
	})
}

// Instance is one process of the cluster.
type Instance struct {
	c *Cluster
	i int
}

// Instance returns process i.
func (c *Cluster) Instance(i int) *Instance { return &Instance{c: c, i: i} }

// Client calls this instance only, whatever its state.
func (n *Instance) Client(tb testing.TB, clientID string) *Client {
	tb.Helper()
	cl := n.c.Client(tb, clientID)
	cl.target = n.c.targets[n.i]
	return cl
}

// Logs is the log of the current run of the instance.
func (n *Instance) Logs() string { return n.c.procs[n.i].log.String() }

// MetricValue reads a counter sample of this instance's admin /metrics, labels
// included, or 0 while the series does not exist.
func (n *Instance) MetricValue(tb testing.TB, sample string) int64 {
	tb.Helper()
	return n.c.metricValue(tb, n.c.targets[n.i], sample)
}

// Kill ends instance i with SIGKILL, an abrupt stop, and waits until it is gone.
func (c *Cluster) Kill(tb testing.TB, i int) {
	tb.Helper()
	p := c.procs[i]
	if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		tb.Fatalf("kill instance %d: %v", i, err)
	}
	<-p.done
}

// Stop ends instance i with SIGTERM and fails tb unless it exits with 0.
func (c *Cluster) Stop(tb testing.TB, i int) {
	tb.Helper()
	if err := c.terminate(c.procs[i]); err != nil {
		tb.Fatalf("instance %d: %v", i, err)
	}
}

// Restart stops instance i if it still runs and starts it again with the base
// environment plus env: Fault(…) arms a fault point, EnvOf(config.Roles{…})
// turns roles off, nothing restores it. It returns when the instance is ready.
func (c *Cluster) Restart(tb testing.TB, i int, env ...map[string]string) {
	tb.Helper()
	if err := c.terminate(c.procs[i]); err != nil {
		tb.Fatalf("instance %d: %v", i, err)
	}
	if err := c.start(context.WithoutCancel(tb.Context()), i, env...); err != nil {
		tb.Fatalf("instance %d: %v", i, err)
	}
}

// Restore brings the cluster back to its base state: every instance that
// exited or runs with an override is restarted with the base environment.
// The crash tests defer it (spec M8 §5.3); when the test failed, the runs
// being replaced are logged first.
func (c *Cluster) Restore(tb testing.TB) {
	tb.Helper()
	if tb.Failed() {
		c.logRuns(tb)
	}
	for i, p := range c.procs {
		if p.exited() || p.custom {
			c.Restart(tb, i)
		}
	}
	c.AssertAllReady(tb)
}

// AssertAllReady fails tb unless every instance runs, disarmed, and answers
// /health/ready with 200.
func (c *Cluster) AssertAllReady(tb testing.TB) {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(tb.Context()), requestTimeout)
	defer cancel()
	for i, p := range c.procs {
		if p.exited() || p.target.armed.Load() || !c.ready(ctx, p.target) {
			tb.Fatalf("instance %d is not back in the cluster (exited %v, armed %v)", i, p.exited(), p.target.armed.Load())
		}
	}
}

// Fault is the override of Restart that arms fault points (test-plan §4).
func Fault(points ...string) map[string]string {
	return map[string]string{"PDA_FAULT": strings.Join(points, ",")}
}

// WaitExit waits for the current run of the instance to exit and returns its
// code: 137 at a fault point, -1 after Kill, 0 after a clean stop.
func (n *Instance) WaitExit(tb testing.TB) int {
	tb.Helper()
	p := n.c.procs[n.i]
	select {
	case <-p.done:
		return p.code
	case <-time.After(exitTimeout):
		tb.Fatalf("instance %d did not exit within %v", n.i, exitTimeout)
		return 0
	}
}

// AssertFaultHit is the proof test-plan §4 requires that the crash happened
// where the test says: the instance exited with 137 and logged FAULT_HIT.
func (n *Instance) AssertFaultHit(tb testing.TB, point string) {
	tb.Helper()
	if code := n.WaitExit(tb); code != faultCode {
		tb.Fatalf("instance %d exited with %d, want %d at the fault point %s:\n%s", n.i, code, faultCode, point, n.Logs())
	}
	if !strings.Contains(n.Logs(), "FAULT_HIT "+point+"\n") {
		tb.Fatalf("instance %d exited with %d but did not log FAULT_HIT %s:\n%s", n.i, faultCode, point, n.Logs())
	}
}
