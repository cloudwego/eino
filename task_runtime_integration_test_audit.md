# Task Runtime Integration Test Audit

## Scope

- Production paths:
  - `adk/internal/taskfirst/coordinator.go`
  - `adk/task/local/local.go`
  - `adk/task/local/stream.go`
  - `adk/task/tool/managed_tool.go`
  - `adk/task/subagent/turn_loop.go`
  - `adk/task/subagent/subagent.go`
  - `adk/task/background/executor.go`
  - `adk/task/background/mailbox.go`
  - `adk/task/background/in_memory_store.go`
- Existing focused tests:
  - `adk/task/subagent/turn_loop_test.go`
  - `adk/task/subagent/runtime_validation_test.go`
  - `adk/task/subagent/progress_test.go`
  - `adk/task/background/mailbox_test.go`
  - `adk/task/background/manager_test.go`
- Added integration tests:
  - `adk/internal/taskfirst/coordinator_test.go`
  - `adk/task/background/publication_test.go`
  - `adk/task/background/event_persister_test.go`
  - task-first cases in `adk/task/local/local_test.go`
  - task-first cases in `adk/task/tool/managed_tool_test.go`
  - typed event persistence in `adk/middlewares/subagent/middleware_test.go`
  - caller-abort coverage in `adk/middlewares/filesystem/bash_run_test.go`
  - `adk/task/subagent/integration_test.go`

The audit treats coverage as a discovery signal. The selection criterion for new
tests is whether a failure would violate a public ownership, durability,
routing, or session invariant across multiple runtime layers.

## Test Catalog

### Existing Controller tests

- `TestControllerForegroundCompletesWithoutBackgroundRecord`
- `TestAttack_InactiveForegroundNotificationSurvivesReplay`
- `TestAttack_ForegroundTerminalCandidateSealsWithoutReplay`
- `TestAttack_ForegroundFailureIsReplayableAndReleasesSession`
- `TestAttack_InactiveForegroundCancelSealsMailbox`
- `TestControllerBackgroundCompletes`
- `TestControllerForegroundInterruptResumesFromMailbox`
- `TestAttack_BackgroundInterruptResumeWakesTask`
- `TestAttack_MultipleResumeInputsFailClosed`
- `TestControllerBarrierWaitThenInput`
- `TestAttack_CancelRacingForegroundHandoffCancelsNewBackgroundTask`
- `TestContinueCreatesNewTaskInPersistentChildSession`
- `TestContinueIdleSessionRequiresStartOptions`
- `TestNestedSubAgentMailboxUsesDirectParent`
- `TestForegroundInputCanPreemptActiveTurn`
- `TestAttack_ReplayIdentityIgnoresFrameworkMessageID`
- `TestControllerValidationAndContextHelpers`
- `TestControllerRejectsInvalidCompletionAndMissingFinal`
- `TestAttack_StartHonorsStreamingAndTerminalCancelIsIdempotent`
- `TestAttack_BackgroundAgentErrorIsDurablyFailed`
- `TestControllerBackgroundControlResults`
- Runtime validation and progress tests in
  `runtime_validation_test.go` and `progress_test.go`

### Added integration tests

| Test | Location | Invariant |
|---|---|---|
| `TestIntegration_ForegroundHandoffConsumesNestedCompletion` | `integration_test.go:297` | A foreground parent may hand off, retain identity, receive a nested completion, and finish under Manager ownership. |
| `TestIntegration_ForegroundConsumesNestedCompletionWithoutHandoff` | `integration_test.go:390` | A running foreground parent consumes nested lifecycle input without creating a background lifecycle record. |
| `TestIntegration_BackgroundResumeSurvivesControllerRestart` | `integration_test.go:448` | A new Controller and Manager can resume a waiting task from durable state with the same TaskID and ChildSessionID. |
| `TestIntegration_ConcurrentContinueKeepsSingleActiveTask` | `integration_test.go:511` | Concurrent continuation of one idle ChildSession creates one active Task and persists every logical input once. |

### Added task-first contract and integration tests

| Area | Tests | Invariant |
|---|---|---|
| Coordinator | `TestExecutionChannelsAreStableAndTimeoutDoesNotReset`, `TestAwaitPublishesOnTimeoutAndLeavesTaskRunning` | Foreground occupancy has one start-armed timer; timeout publication does not replace or restart the Manager attempt. |
| Coordinator | `TestAwaitRejectedTimeoutWaitsForCanceledTerminal`, `TestAwaitCallerAbortPolicy` | Timeout rejection waits for a terminal canceled snapshot; caller abort defaults to detach and only cancels by explicit policy. |
| Coordinator | `TestExecutionObservesAttemptClaimedByAnotherManager` | A competing Manager claim does not turn a valid authoritative attempt into a foreground error. |
| Publication | `TestManagerDeferredPublicationSkipsImmediateCreatedEvent`, `TestAttack_PublishAndCompletionHaveOneVisibilityOutcome`, `TestAttack_PublishAndCancellationHaveOneVisibilityOutcome`, `TestPublishNestedTaskFailsBeforeParentMutation` | Deferred tasks stay hidden until publish; publish races have one visibility outcome; nested publication cannot partially mutate a sealed parent. |
| Local | `TestRunnerTaskFirstCallerAbortDetaches`, `TestRunnerTaskFirstForegroundCompletionStaysDeferred`, `TestRunnerTaskFirstCancelStopsUnderlyingWork` | Local work is Manager-owned from Attempt 1, foreground completion stays hidden, and cancellation reaches the real work context. |
| Managed Tool | `TestManagedToolPlainToolCanAutoBackgroundTaskFirst`, `TestManagedToolTaskFirstCallerAbortPolicy`, `TestManagedToolTaskFirstWaitingInputResumesSameTask`, `TestManagedToolTaskFirstForegroundCompletionStaysDeferred` | Plain and recoverable tools share task-first ownership, waiting-input resumes the same Task, and foreground completion emits no lifecycle notification. |
| Filesystem | `TestManagedExecuteTool_CallerAbortDetachesTaskOwnedShell` | Caller cancellation detaches the shell projection without canceling the task-owned command. |
| Event persistence | `TestPersistTaskEventPassesTypedEventAndStream`, `TestTaskEventStreamErrorCanReplayPersistedPrefix`, `TestTaskEventWriterFencesEveryStreamPart`, `TestTaskEventFinalPartClosesLogicalEvent`, `TestAttack_ConcurrentFinalPartClosesEventOnce` | Persisters receive typed events and stream copies; durable parts are replayable, finality-aware, and attempt-fenced. |
| Event persistence | `TestRunnerStreamUsesConfiguredEventPersister`, `TestManagedToolUsesRegistrationEventPersister`, `TestAgentEventPersisterReceivesRawEventAndSeparateStream` | Local, Tool, and Local Sub-agent callers can own serialization without consuming the live stream. |

## 1. Duplicate Analysis

No true or near-duplicate tests were added.

| Test A | Test B | Relationship | Action |
|---|---|---|---|
| `turn_loop_test.go:611` same-process background resume | `integration_test.go:448` reconstructed Controller/Manager resume | Intentional pair: process-local wake versus durable reconstruction | Keep both |
| `turn_loop_test.go:835` nested mailbox metadata | `integration_test.go:297` nested completion after handoff | Intentional layering: registration contract versus full lifecycle | Keep both |
| `integration_test.go:297` notification after handoff | `integration_test.go:390` notification while attached | Intentional owner-path pair | Keep both |
| `turn_loop_test.go:760` sequential continuation | `integration_test.go:511` concurrent first continuation | Intentional boundary pair | Keep both |

## 2. Assertion Quality

The new tests assert exact lifecycle states, attempts, mailbox cursor values,
input counts, routing targets, and identities. They do not use timing as the
result assertion; `require.Eventually` is only used to observe asynchronous
state transitions with a bounded deadline.

One existing weak assertion remains:

```text
Location: adk/task/subagent/turn_loop_test.go:809
Current:  require.GreaterOrEqual(t, len(events.Events), 4)
Reason:   Session internals may add framework events, so an exact total is not
          a stable public contract.
Action:   Keep; stronger Task and Mailbox assertions are now provided by the
          integration tests.
```

## 3. Boilerplate

The new file centralizes repeated infrastructure:

| Helper | Location | Reuse |
|---|---|---|
| `newIntegrationManager` | `integration_test.go:234` | Shared lifecycle and mailbox Store wiring |
| `newIntegrationController` | `integration_test.go:249` | Shared Controller, SessionStore, and executor registration |
| `closeIntegrationManager` | `integration_test.go:268` | Bounded Manager shutdown |
| `awaitIntegrationValue` | `integration_test.go:206` | Bounded cross-goroutine synchronization |

The remaining setup is scenario-specific and keeping it inline makes owner
transitions and persisted state visible to readers.

## 4. Logical Grouping

```text
TestIntegration/
|-- Foreground/
|   |-- NestedCompletionWhileAttached
|   `-- NestedCompletionAfterHandoff
|-- Recovery/
|   `-- WaitingInputAcrossControllerRestart
`-- ChildSession/
    `-- ConcurrentContinueCreatesOneActiveTask
```

Separate top-level test functions are retained so race and stress runs can
target each state machine independently.

## 5. Semantic Value

All four tests should be kept:

- They cross package boundaries through public Controller and Manager APIs.
- They use real TurnLoop checkpoints and SessionStore state.
- They exercise real mailbox transitions rather than mutating snapshots.
- They verify negative invariants: no background parent record while attached,
  no child notification leakage to the root outbox, and no stale-owner writes.
- They verify exact durable outcomes after asynchronous execution.

No getter-only or coverage-only tests were added.

## 6. Important Coverage Gaps

### Closed: nested completion across ownership transfer

```text
Production:
  adk/task/subagent/turn_loop.go:666
  adk/task/subagent/turn_loop.go:856
  adk/task/background/mailbox.go:39
  adk/task/background/in_memory_store.go:1006
Risk:
  A child completes after its foreground parent returns, but the parent is
  never resumed or the notification is sent to the root session.
Test:
  TestIntegration_ForegroundHandoffConsumesNestedCompletion
```

This test also proves that the pre-handoff `ExecutionContext` is rejected after
the mailbox generation changes.

### Closed: nested completion while the parent is still attached

```text
Production:
  adk/task/subagent/turn_loop.go:987
  adk/task/background/executor.go:719
  adk/task/background/in_memory_store.go:1006
Risk:
  A nested completion is persisted but not consumed by the active foreground
  TurnLoop, or unnecessarily forces the parent into background lifecycle.
Test:
  TestIntegration_ForegroundConsumesNestedCompletionWithoutHandoff
```

### Closed: waiting-input recovery after runtime reconstruction

```text
Production:
  adk/task/subagent/turn_loop.go:223
  adk/task/subagent/turn_loop.go:547
  adk/task/subagent/turn_loop.go:732
  adk/task/subagent/turn_loop.go:892
Risk:
  Checkpoint, TaskID, or ChildSessionID is process-local and a replacement
  worker cannot resume the interrupted agent.
Test:
  TestIntegration_BackgroundResumeSurvivesControllerRestart
```

### Closed: concurrent ChildSession admission

```text
Production:
  adk/task/subagent/turn_loop.go:480
  adk/task/background/mailbox.go:390
  adk/task/background/mailbox.go:455
Risk:
  Concurrent Continue calls create multiple active Tasks for one ChildSession,
  lose inputs, or reuse one EventID for different messages.
Test:
  TestIntegration_ConcurrentContinueKeepsSingleActiveTask
```

### Closed: publication and terminal/cancel races

```text
Production:
  adk/task/background/executor.go
  adk/task/background/in_memory_store.go
Risk:
  Publishing invalidates the active attempt, emits duplicate visibility events,
  or exposes a nested task after its direct parent mailbox is sealed.
Tests:
  TestAttack_PublishAndCompletionHaveOneVisibilityOutcome
  TestAttack_PublishAndCancellationHaveOneVisibilityOutcome
  TestPublishNestedTaskFailsBeforeParentMutation
```

### Closed: foreground observer lifetime versus task lifetime

```text
Production:
  adk/internal/taskfirst/coordinator.go
  adk/task/local/stream.go
  adk/task/tool/managed_tool.go
Risk:
  Update traffic resets timeout, caller cancellation kills Manager-owned work,
  timeout rejection returns a nonterminal snapshot, or a competing claim is
  reported as task failure.
Tests:
  TestExecutionChannelsAreStableAndTimeoutDoesNotReset
  TestAwaitRejectedTimeoutWaitsForCanceledTerminal
  TestAwaitCallerAbortPolicy
  TestExecutionObservesAttemptClaimedByAnotherManager
  TestRunnerTaskFirstCallerAbortDetaches
  TestManagedToolTaskFirstCallerAbortPolicy
```

### Residual system-test boundary

The tests use the in-memory implementations of `LifecycleStore`,
`SessionEventStore`, and checkpoint storage. Provider conformance suites cover
SPI behavior, but a deployment should additionally run the same restart and
concurrent-admission scenarios against its real database implementation and
worker scheduler. That belongs in provider/system tests, not this package.

### Closed: typed streaming event persistence

```text
Production:
  adk/task/background/executor.go
  adk/task/background/in_memory_store.go
  adk/middlewares/subagent/agent_tool.go
Risk:
  Executor code serializes events before the persistence boundary, live and
  durable consumers race on one stream, or a stale attempt appends later parts
  after ownership changes.
Tests:
  TestPersistTaskEventPassesTypedEventAndStream
  TestTaskEventStreamErrorCanReplayPersistedPrefix
  TestTaskEventWriterFencesEveryStreamPart
  TestTaskEventFinalPartClosesLogicalEvent
  TestAttack_ConcurrentFinalPartClosesEventOnce
  TestAgentEventPersisterReceivesRawEventAndSeparateStream
```

## Coverage

- `go test -coverpkg=./adk/task/... ./adk/task/...`: 81.1% aggregate Task
  runtime coverage.
- `go test -coverprofile=/tmp/subagent-integration.cover
  ./adk/task/subagent -run '^TestIntegration_'`: the four integration tests
  alone execute 49.9% of the Sub-agent package.
- Full `adk/task/subagent` package coverage after the additions: 75.6%.

The remaining uncovered lines are dominated by validation branches, trivial
forwarders, optional progress formatting, injected storage failures, and the
new policy-only `adk/task/foreground` package. They do not justify additional
broad integration scenarios.

## Summary

| Priority | Issue | Count | Result |
|---|---|---:|---|
| High | Missing owner-transfer and nested-notification integration coverage | 2 | Fixed |
| High | Missing process-reconstruction resume coverage | 1 | Fixed |
| High | Missing concurrent ChildSession admission coverage | 1 | Fixed |
| High | Missing task-first publication/terminal race coverage | 3 | Fixed |
| High | Missing caller-abort and timeout policy coverage | 4 | Fixed |
| High | Missing typed streaming event persistence coverage | 5 | Fixed |
| Medium | Real provider and scheduler process test | 1 | Deferred to deployment integration suite |
| Low | Weak assertion over framework-owned event count | 1 | Kept with justification |
