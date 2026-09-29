/*
 * Copyright 2026 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package adk

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestMessageEventType(t *testing.T) {
	tests := []struct {
		name      string
		role      schema.RoleType
		streaming bool
		want      AgentEventType
	}{
		{"model end", schema.Assistant, false, AgentEventModelEnd},
		{"model delta", schema.Assistant, true, AgentEventModelDelta},
		{"tool end", schema.Tool, false, AgentEventToolEnd},
		{"tool delta", schema.Tool, true, AgentEventToolDelta},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := messageEventType(tt.role, tt.streaming); got != tt.want {
				t.Fatalf("messageEventType(%q, %v) = %q, want %q", tt.role, tt.streaming, got, tt.want)
			}
		})
	}
}

func TestEventFromMessageSetsType(t *testing.T) {
	msg := schema.AssistantMessage("hi", nil)

	if e := EventFromMessage(msg, nil, schema.Assistant, ""); e.Type != AgentEventModelEnd {
		t.Fatalf("assistant message event type = %q, want %q", e.Type, AgentEventModelEnd)
	}

	if e := EventFromMessage(msg, nil, schema.Tool, "lookup"); e.Type != AgentEventToolEnd {
		t.Fatalf("tool message event type = %q, want %q", e.Type, AgentEventToolEnd)
	}
}

func TestEventFromAgenticMessageSetsType(t *testing.T) {
	msg := &schema.AgenticMessage{}

	if e := EventFromAgenticMessage(msg, nil, schema.AgenticRoleTypeAssistant); e.Type != AgentEventModelEnd {
		t.Fatalf("assistant agentic event type = %q, want %q", e.Type, AgentEventModelEnd)
	}

	if e := EventFromAgenticMessage(msg, nil, schema.AgenticRoleTypeUser); e.Type != AgentEventToolEnd {
		t.Fatalf("tool agentic event type = %q, want %q", e.Type, AgentEventToolEnd)
	}
}

func TestActionEventType(t *testing.T) {
	tests := []struct {
		name   string
		action *AgentAction
		want   AgentEventType
	}{
		{"nil", nil, AgentEventUnknown},
		{"exit", &AgentAction{Exit: true}, AgentEventAgentEnd},
		{"transfer", &AgentAction{TransferToAgent: &TransferToAgentAction{DestAgentName: "a"}}, AgentEventTransfer},
		{"interrupt", &AgentAction{Interrupted: &InterruptInfo{}}, AgentEventInterrupt},
		{"break loop", &AgentAction{BreakLoop: &BreakLoopAction{}}, AgentEventBreakLoop},
		{"customized only", &AgentAction{CustomizedAction: struct{}{}}, AgentEventUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := actionEventType(tt.action); got != tt.want {
				t.Fatalf("actionEventType() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInterruptEventSetsType(t *testing.T) {
	e := Interrupt(context.Background(), "need input")
	if e.Type != AgentEventInterrupt {
		t.Fatalf("interrupt event type = %q, want %q", e.Type, AgentEventInterrupt)
	}
	if e.Action == nil || e.Action.Interrupted == nil {
		t.Fatalf("interrupt event missing Interrupted action: %+v", e)
	}
}
