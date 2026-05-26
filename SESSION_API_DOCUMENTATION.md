<!--
Copyright 2026 CloudWeGo Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# Session API Documentation

## Agent Interrupt Events

`SessionEventAgentInterrupt` persists an agent-initiated business interrupt in the managed session timeline.

```go
const SessionEventAgentInterrupt SessionEventKind = "agent.interrupt"
```

The event is distinct from `SessionEventUserInterrupt`:

- `SessionEventAgentInterrupt` means the agent paused execution and needs external input before it can resume.
- `SessionEventUserInterrupt` means the user proactively cancelled execution.

The payload is stored on `SessionEvent.AgentInterrupt`.

```go
type AgentInterruptEvent struct {
    Cause             AgentInterruptCause `json:"cause,omitempty"`
    CheckPointID      string              `json:"checkpoint_id,omitempty"`
    InterruptContexts []*InterruptCtx     `json:"interrupt_contexts,omitempty"`
    ToolUseID         string              `json:"tool_use_id,omitempty"`
    SpanEventID       string              `json:"span_event_id,omitempty"`
}
```

`Cause` categorizes why the interrupt happened:

```go
const (
    AgentInterruptCauseToolPermission AgentInterruptCause = "tool_permission"
    AgentInterruptCauseCustomTool     AgentInterruptCause = "custom_tool"
    AgentInterruptCauseGeneric        AgentInterruptCause = "generic"
)
```

`CheckPointID` is the checkpoint key passed to `Runner.Resume` or `Runner.ResumeWithParams`.

`InterruptContexts` uses the same public `[]*InterruptCtx` shape exposed on live `AgentAction.Interrupted` events. Root-cause `InterruptCtx.ID` values are the normal keys for `ResumeParams.Targets`.

```go
resumeParams := &ResumeParams{
    Targets: map[string]any{
        interruptEvent.InterruptContexts[0].ID: result,
    },
}
```

`ToolUseID` is populated when the root-cause interrupt address contains a tool segment. The runner uses `AddressSegment.SubID` when present and falls back to `AddressSegment.ID` for compatibility with older tool-address paths.

`SpanEventID` is a best-effort link to the related `span.tool_call_start` session event. It may be empty when the runner has not observed a matching tool span start event in the current run or resume drain loop.
