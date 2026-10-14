package pipeline

import (
	"context"
	"fmt"
)

// parallelTaskOutcome describes a scheduling dependency, not a step's retry
// policy. The canonical executor resolves retries and success hooks before
// returning an outcome to this scheduler.
type parallelTaskOutcome uint8

const (
	parallelTaskPending parallelTaskOutcome = iota
	parallelTaskSucceeded
	parallelTaskFailed
	parallelTaskBlocked
	parallelTaskCancelled
)

type parallelTask struct {
	id        string
	aliases   []string
	dependsOn []string
}

type parallelTaskGraph struct {
	tasks             []parallelTask
	dependencies      [][]int
	externalBlocked   [][]string
	externalCancelled [][]string
	order             []int
}

// newParallelTaskGraph resolves author IDs and materialized IDs without
// rewriting the workflow or the step definitions used by recovery receipts.
// External dependencies must already be settled: the outer scheduler owns
// those dependencies, and a nested body must not silently bypass that gate.
func newParallelTaskGraph(tasks []parallelTask, external map[string]parallelTaskOutcome) (*parallelTaskGraph, error) {
	g := &parallelTaskGraph{
		tasks:             tasks,
		dependencies:      make([][]int, len(tasks)),
		externalBlocked:   make([][]string, len(tasks)),
		externalCancelled: make([][]string, len(tasks)),
	}
	byName, err := indexParallelTasks(tasks, external)
	if err != nil {
		return nil, err
	}

	indegree := make([]int, len(tasks))
	reverse := make([][]int, len(tasks))
	for i, task := range tasks {
		seen := make(map[string]bool)
		seenSibling := make(map[int]bool)
		for _, dep := range task.dependsOn {
			if seen[dep] {
				continue
			}
			seen[dep] = true
			if j, ok := byName[dep]; ok {
				if !seenSibling[j] {
					seenSibling[j] = true
					g.dependencies[i] = append(g.dependencies[i], j)
					reverse[j] = append(reverse[j], i)
					indegree[i]++
				}
				continue
			}
			outcome, ok := external[dep]
			if !ok {
				return nil, fmt.Errorf("parallel child %q depends on unknown step %q", task.id, dep)
			}
			switch outcome {
			case parallelTaskSucceeded:
			case parallelTaskFailed, parallelTaskBlocked:
				g.externalBlocked[i] = append(g.externalBlocked[i], dep)
			case parallelTaskCancelled:
				g.externalCancelled[i] = append(g.externalCancelled[i], dep)
			default:
				return nil, fmt.Errorf("parallel child %q depends on unfinished external step %q", task.id, dep)
			}
		}
	}

	// Validate every edge before any child is adopted, checkpointed or run.
	// Declaration order breaks ties but never substitutes for dependency order.
	queue := make([]int, 0, len(tasks))
	for i, n := range indegree {
		if n == 0 {
			queue = append(queue, i)
		}
	}
	for len(queue) > 0 {
		i := queue[0]
		queue = queue[1:]
		g.order = append(g.order, i)
		for _, next := range reverse[i] {
			indegree[next]--
			if indegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	if len(g.order) != len(tasks) {
		var blocked []string
		for i, n := range indegree {
			if n > 0 {
				blocked = append(blocked, tasks[i].id)
			}
		}
		return nil, fmt.Errorf("parallel child dependency cycle: %v", blocked)
	}
	return g, nil
}

// indexParallelTasks is shared with static graph construction. Both admission
// paths must resolve the same names and reject aliases that could address two
// different children or a top-level producer.
func indexParallelTasks(tasks []parallelTask, external map[string]parallelTaskOutcome) (map[string]int, error) {
	byName := make(map[string]int)
	for i, task := range tasks {
		if task.id == "" {
			return nil, fmt.Errorf("parallel child %d has no runtime identity", i+1)
		}
		for _, name := range append([]string{task.id}, task.aliases...) {
			if name == "" {
				continue
			}
			if previous, ok := byName[name]; ok && previous != i {
				return nil, fmt.Errorf("parallel child identity %q is ambiguous between %q and %q", name, tasks[previous].id, task.id)
			}
			if _, ok := external[name]; ok {
				return nil, fmt.Errorf("parallel child identity %q conflicts with an external step", name)
			}
			byName[name] = i
		}
	}
	return byName, nil
}

// validateRetained refuses inconsistent recovery before mutation or dispatch.
func (g *parallelTaskGraph) validateRetained(retained map[int]bool) error {
	for i := range retained {
		if i < 0 || i >= len(g.tasks) {
			return fmt.Errorf("invalid retained parallel child index %d", i)
		}
	}
	// Retaining a consumer while replaying its producer would silently use
	// stale effects/output. Do not guess whether the consumer may be repeated.
	for i := range g.tasks {
		if !retained[i] {
			continue
		}
		if len(g.externalBlocked[i]) > 0 || len(g.externalCancelled[i]) > 0 {
			return fmt.Errorf("retained parallel child %q has an unsatisfied external dependency", g.tasks[i].id)
		}
		for _, dep := range g.dependencies[i] {
			if !retained[dep] {
				return fmt.Errorf("retained parallel child %q depends on unfinished child %q; refusing stale recovery", g.tasks[i].id, g.tasks[dep].id)
			}
		}
	}
	return nil
}

// run admits only ready children. Waiting dependencies consume neither a
// worker slot nor a goroutine. start/settle run serially on the scheduler;
// run is bounded by limit. A dependency is released ONLY after settle returns
// successfully, so its output and recovery checkpoint precede dependent work.
// All started children are joined, including on cancellation or storage failure.
func (g *parallelTaskGraph) run(
	ctx context.Context,
	limit int,
	failFast bool,
	retained map[int]bool,
	start func(int) error,
	run func(context.Context, int) parallelTaskOutcome,
	settle func(int, parallelTaskOutcome, []string) error,
) error {
	if limit < 1 {
		return fmt.Errorf("parallel concurrency limit must be positive")
	}
	if err := g.validateRetained(retained); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	outcomes := make([]parallelTaskOutcome, len(g.tasks))
	started := make([]bool, len(g.tasks))
	type completion struct {
		index   int
		outcome parallelTaskOutcome
	}
	if limit > len(g.tasks) {
		limit = len(g.tasks)
	}
	done := make(chan completion, limit)
	active, remaining := 0, len(g.tasks)
	var firstErr error
	record := func(i int, outcome parallelTaskOutcome, blocked []string) {
		if err := settle(i, outcome, blocked); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			cancel()
		}
		outcomes[i] = outcome
		remaining--
		if failFast && (outcome == parallelTaskFailed || outcome == parallelTaskBlocked) {
			cancel()
		}
	}

	// Rebuild retained outputs in dependency order before admitting new work.
	for _, i := range g.order {
		if retained[i] {
			record(i, parallelTaskSucceeded, nil)
		}
	}
	for remaining > 0 {
		for i := range g.tasks {
			if started[i] || outcomes[i] != parallelTaskPending {
				continue
			}
			if ctx.Err() != nil {
				record(i, parallelTaskCancelled, nil)
				continue
			}
			ready := true
			blocked := append([]string(nil), g.externalBlocked[i]...)
			dependencyCancelled := len(g.externalCancelled[i]) > 0
			for _, dep := range g.dependencies[i] {
				switch outcomes[dep] {
				case parallelTaskPending:
					ready = false
				case parallelTaskSucceeded:
				case parallelTaskCancelled:
					dependencyCancelled = true
				default:
					blocked = append(blocked, g.tasks[dep].id)
				}
			}
			// A failed prerequisite is already conclusive, even if another
			// prerequisite is still running. Propagate it transitively.
			if len(blocked) > 0 {
				record(i, parallelTaskBlocked, blocked)
				continue
			}
			if dependencyCancelled {
				// Cancellation is not a failed prerequisite that continue may
				// ignore. Preserve it through the complete dependency chain.
				record(i, parallelTaskCancelled, nil)
				continue
			}
			if !ready || active >= limit {
				continue
			}
			if err := start(i); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				cancel()
				record(i, parallelTaskCancelled, nil)
				continue
			}
			if ctx.Err() != nil {
				record(i, parallelTaskCancelled, nil)
				continue
			}
			started[i] = true
			active++
			go func(index int) {
				outcome := parallelTaskCancelled
				if ctx.Err() == nil {
					outcome = run(ctx, index)
				}
				done <- completion{index: index, outcome: outcome}
			}(i)
		}
		if active == 0 {
			// Propagating a blocked child may unblock an earlier declaration
			// for skipping. A validated acyclic graph always makes progress.
			continue
		}
		completed := <-done
		active--
		switch completed.outcome {
		case parallelTaskSucceeded, parallelTaskFailed, parallelTaskBlocked, parallelTaskCancelled:
		default:
			if firstErr == nil {
				firstErr = fmt.Errorf("parallel child %q returned an invalid scheduling outcome", g.tasks[completed.index].id)
			}
			cancel()
			completed.outcome = parallelTaskFailed
		}
		record(completed.index, completed.outcome, nil)
	}
	return firstErr
}
