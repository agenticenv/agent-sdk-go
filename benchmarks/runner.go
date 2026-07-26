package main

import (
	"context"
	"math/rand"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/agenticenv/agent-sdk-go/benchmarks/setup"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"github.com/agenticenv/agent-sdk-go/pkg/memory"
)

type runOutcome struct {
	latencyMs     float64
	success       bool
	memoryRecalls int64
	memoryStores  int64
}

func runBenchmark(ctx context.Context, cfg *setup.Config, llm *setup.MockLLMClient, lgr logger.Logger, runRng *rand.Rand) (*BenchmarkMetrics, error) {
	poolSize := 1
	if cfg.Agent.Concurrent {
		poolSize = cfg.Agent.ConcurrentCount
	}

	bundles, err := buildAgentPool(cfg, llm, lgr, poolSize)
	if err != nil {
		return nil, err
	}
	defer func() {
		for _, b := range bundles {
			setup.CloseAgents(b.All)
		}
	}()

	var memBefore runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&memBefore)

	cpuBefore, err := processCPUTimeMs()
	if err != nil {
		return nil, err
	}

	outcomes := make([]runOutcome, 0, cfg.Agent.Runs)
	var outcomesMu sync.Mutex

	remaining := cfg.Agent.Runs
	for remaining > 0 {
		batchSize := 1
		if cfg.Agent.Concurrent {
			batchSize = cfg.Agent.ConcurrentCount
			if batchSize > remaining {
				batchSize = remaining
			}
		}

		var wg sync.WaitGroup
		var batchErr atomic.Value
		for i := 0; i < batchSize; i++ {
			wg.Add(1)
			agentIdx := i % len(bundles)
			go func(bundle *AgentBundle) {
				defer wg.Done()
				outcome := executeRun(ctx, cfg, bundle.Root, runRng)
				outcomesMu.Lock()
				outcomes = append(outcomes, outcome)
				outcomesMu.Unlock()
			}(bundles[agentIdx])
		}
		wg.Wait()
		if errVal := batchErr.Load(); errVal != nil {
			return nil, errVal.(error)
		}
		remaining -= batchSize
	}

	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)

	cpuAfter, err := processCPUTimeMs()
	if err != nil {
		return nil, err
	}

	inputTokens, outputTokens := llm.Stats().Snapshot()
	return aggregateMetrics(outcomes, memBefore, memAfter, cpuAfter-cpuBefore, inputTokens, outputTokens), nil
}

func executeRun(ctx context.Context, cfg *setup.Config, a *agent.Agent, rng *rand.Rand) runOutcome {
	runCtx := ctx
	if cfg.MemoryEnabled() {
		runCtx = memory.WithContextUserID(ctx, cfg.Memory.UserID)
	}
	start := time.Now()
	agentRun, runErr := a.Run(runCtx, setup.RandomUserPrompt(rng), nil)
	if runErr != nil {
		return runOutcome{latencyMs: float64(time.Since(start).Milliseconds()), success: false}
	}
	result, err := agentRun.Get(runCtx)
	outcome := runOutcome{
		latencyMs: float64(time.Since(start).Milliseconds()),
		success:   err == nil,
	}
	if result != nil && result.Telemetry != nil {
		outcome.memoryRecalls = result.Telemetry.Storage.TotalMemoryRecalls
		outcome.memoryStores = result.Telemetry.Storage.TotalMemoryStores
	}
	return outcome
}

func processCPUTimeMs() (float64, error) {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0, err
	}
	user := float64(usage.Utime.Sec)*1000 + float64(usage.Utime.Usec)/1000
	sys := float64(usage.Stime.Sec)*1000 + float64(usage.Stime.Usec)/1000
	return user + sys, nil
}
