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

package main

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

type recordingResumer struct {
	resumeCalls           int
	resumeWithParamsCalls int
	targets               map[string]any
	iter                  *adk.AsyncIterator[*adk.AgentEvent]
	err                   error
}

func (r *recordingResumer) Resume(context.Context, string,
	...adk.AgentRunOption) (*adk.AsyncIterator[*adk.AgentEvent], error) {
	r.resumeCalls++
	return r.iter, r.err
}

func (r *recordingResumer) ResumeWithParams(_ context.Context, _ string,
	params *adk.ResumeParams, _ ...adk.AgentRunOption) (*adk.AsyncIterator[*adk.AgentEvent], error) {
	r.resumeWithParamsCalls++
	r.targets = params.Targets
	return r.iter, r.err
}

func TestChatModelResponse(t *testing.T) {
	model := &chatModel{toolNames: []string{"first", "second"}}
	bound, err := model.WithTools(nil)
	if err != nil {
		t.Fatal(err)
	}
	if bound != model {
		t.Fatalf("bound model = %T, want original model", bound)
	}

	message, err := model.Generate(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(message.ToolCalls) != 2 ||
		message.ToolCalls[0].Function.Name != "first" ||
		message.ToolCalls[1].Function.Name != "second" {
		t.Fatalf("generated tool calls = %#v", message.ToolCalls)
	}

	stream, err := model.Stream(context.Background(), []*schema.Message{
		schema.ToolMessage("result", "call-0"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	completed, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if completed.Role != schema.Assistant || completed.Content != "completed" {
		t.Fatalf("streamed message = %#v, want terminal assistant completion", completed)
	}
}

func TestResumeFixture(t *testing.T) {
	t.Run("implicit resume uses Runner.Resume", func(t *testing.T) {
		var selected fixture
		err := json.Unmarshal([]byte(`{
			"name":"parallel_6",
			"implicit_resume":true,
			"resume_target_count":2,
			"interrupt_ids":["first","second"]
		}`), &selected)
		if err != nil {
			t.Fatal(err)
		}
		if !selected.ImplicitResume {
			t.Fatal("implicit_resume was not parsed")
		}

		runner := &recordingResumer{}
		_, method, err := resumeFixture(context.Background(), runner, selected, 2)
		if err != nil {
			t.Fatal(err)
		}
		if method != resumeMethodImplicit {
			t.Fatalf("method = %q, want %q", method, resumeMethodImplicit)
		}
		if runner.resumeCalls != 1 || runner.resumeWithParamsCalls != 0 {
			t.Fatalf("Resume calls = %d, ResumeWithParams calls = %d, want 1 and 0",
				runner.resumeCalls, runner.resumeWithParamsCalls)
		}
	})

	t.Run("targeted resume uses Runner.ResumeWithParams", func(t *testing.T) {
		selected := fixture{
			Name:              "parallel_6_multi_target",
			ResumeTargetCount: 2,
			InterruptIDs:      []string{"first", "second", "third"},
		}
		runner := &recordingResumer{}
		_, method, err := resumeFixture(context.Background(), runner, selected, 2)
		if err != nil {
			t.Fatal(err)
		}
		if method != resumeMethodTargeted {
			t.Fatalf("method = %q, want %q", method, resumeMethodTargeted)
		}
		if runner.resumeCalls != 0 || runner.resumeWithParamsCalls != 1 {
			t.Fatalf("Resume calls = %d, ResumeWithParams calls = %d, want 0 and 1",
				runner.resumeCalls, runner.resumeWithParamsCalls)
		}
		wantTargets := map[string]any{"first": "resumed", "second": "resumed"}
		if !reflect.DeepEqual(runner.targets, wantTargets) {
			t.Fatalf("targets = %#v, want %#v", runner.targets, wantTargets)
		}
	})

	t.Run("rejects invalid target count", func(t *testing.T) {
		selected := fixture{
			Name:               "invalid",
			ResumeTargetCount:  2,
			InterruptIDs:       []string{"only"},
			InterruptAddresses: []string{"only"},
		}
		_, err := validateFixture(selected)
		wantErr := `fixture "invalid" resume target count 2 exceeds interrupt IDs length 1`
		if err == nil || err.Error() != wantErr {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestRun(t *testing.T) {
	dependencies := defaultRunDependencies()

	t.Run("requires fixture selection", func(t *testing.T) {
		err := run(nil, &bytes.Buffer{}, dependencies)
		if err == nil || err.Error() != "-fixture-dir and -fixture are required" {
			t.Fatalf("error = %v, want required flags", err)
		}
	})

	t.Run("decodes gob envelope", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "value.gob")
		var encoded bytes.Buffer
		if err := gob.NewEncoder(&encoded).Encode(anyEnvelope{Value: "legacy"}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := run([]string{"-gob-any-file", path}, &bytes.Buffer{}, dependencies); err != nil {
			t.Fatal(err)
		}
	})

	fixtureDir, err := filepath.Abs("../main_60e1d992")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name                string
		fixture             string
		resumeMethod        string
		terminalOutputs     int
		remainingInterrupts int
	}{
		{
			name:            "targeted full resume",
			fixture:         "single_invoke",
			resumeMethod:    resumeMethodTargeted,
			terminalOutputs: 1,
		},
		{
			name:            "streaming full resume",
			fixture:         "single_stream",
			resumeMethod:    resumeMethodTargeted,
			terminalOutputs: 1,
		},
		{
			name:            "cancel completion resume",
			fixture:         "cancel_after_model",
			resumeMethod:    resumeMethodTargeted,
			terminalOutputs: 1,
		},
		{
			name:            "nested depth completion",
			fixture:         "agent_tool_depth_3",
			resumeMethod:    resumeMethodTargeted,
			terminalOutputs: 1,
		},
		{
			name:            "implicit full resume",
			fixture:         "parallel_6",
			resumeMethod:    resumeMethodImplicit,
			terminalOutputs: 1,
		},
		{
			name:                "targeted partial resume",
			fixture:             "parallel_6_multi_target",
			resumeMethod:        resumeMethodTargeted,
			remainingInterrupts: 4,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer
			err := run([]string{
				"-fixture-dir", fixtureDir,
				"-fixture", tt.fixture,
			}, &stdout, dependencies)
			if err != nil {
				t.Fatal(err)
			}
			var outcome resumeOutcome
			if err = json.Unmarshal(stdout.Bytes(), &outcome); err != nil {
				t.Fatalf("decode output %q: %v", stdout.String(), err)
			}
			if outcome.ResumeMethod != tt.resumeMethod {
				t.Fatalf("resume method = %q, want %q",
					outcome.ResumeMethod, tt.resumeMethod)
			}
			if outcome.TerminalOutputs != tt.terminalOutputs {
				t.Fatalf("terminal outputs = %d, want %d",
					outcome.TerminalOutputs, tt.terminalOutputs)
			}
			if len(outcome.RemainingInterruptAddresses) != tt.remainingInterrupts {
				t.Fatalf("remaining interrupts = %v, want %d",
					outcome.RemainingInterruptAddresses, tt.remainingInterrupts)
			}
		})
	}
}

func TestRunFailureModes(t *testing.T) {
	t.Run("rejects invalid arguments", func(t *testing.T) {
		err := run([]string{"-unknown"}, &bytes.Buffer{}, defaultRunDependencies())
		if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
			t.Fatalf("error = %v, want invalid flag", err)
		}
	})

	readErr := errors.New("injected read failure")
	t.Run("returns manifest read failure", func(t *testing.T) {
		dependencies := defaultRunDependencies()
		dependencies.readFile = func(string) ([]byte, error) {
			return nil, readErr
		}
		err := run([]string{"-fixture-dir", "fixtures", "-fixture", "selected"},
			&bytes.Buffer{}, dependencies)
		if !errors.Is(err, readErr) {
			t.Fatalf("error = %v, want %v", err, readErr)
		}
	})

	t.Run("rejects malformed manifest", func(t *testing.T) {
		dependencies := defaultRunDependencies()
		dependencies.readFile = func(string) ([]byte, error) {
			return []byte("not-json"), nil
		}
		err := run([]string{"-fixture-dir", "fixtures", "-fixture", "selected"},
			&bytes.Buffer{}, dependencies)
		if err == nil || !strings.Contains(err.Error(), "invalid character") {
			t.Fatalf("error = %v, want malformed JSON", err)
		}
	})

	t.Run("rejects missing fixture", func(t *testing.T) {
		dependencies := defaultRunDependencies()
		dependencies.readFile = func(string) ([]byte, error) {
			return []byte(`{"fixtures":[]}`), nil
		}
		err := run([]string{"-fixture-dir", "fixtures", "-fixture", "selected"},
			&bytes.Buffer{}, dependencies)
		if err == nil || err.Error() != `fixture "selected" not found` {
			t.Fatalf("error = %v, want missing fixture", err)
		}
	})

	t.Run("rejects invalid fixture metadata before resume work", func(t *testing.T) {
		tests := []struct {
			name    string
			fixture string
			wantErr string
		}{
			{
				name: "negative depth",
				fixture: `{
					"name":"selected",
					"file":"selected.bin.gz",
					"depth":-1
				}`,
				wantErr: `fixture "selected" has invalid depth -1`,
			},
			{
				name: "negative parallel children",
				fixture: `{
					"name":"selected",
					"file":"selected.bin.gz",
					"parallel_children":-1
				}`,
				wantErr: `fixture "selected" has invalid parallel children -1`,
			},
			{
				name: "negative resume target count",
				fixture: `{
					"name":"selected",
					"file":"selected.bin.gz",
					"resume_target_count":-1
				}`,
				wantErr: `fixture "selected" has invalid resume target count -1`,
			},
			{
				name: "resume target count exceeds interrupt IDs",
				fixture: `{
					"name":"selected",
					"file":"selected.bin.gz",
					"resume_target_count":2,
					"interrupt_ids":["only"],
					"interrupt_addresses":["first","second"]
				}`,
				wantErr: `fixture "selected" resume target count 2 exceeds interrupt IDs length 1`,
			},
			{
				name: "resume target count exceeds interrupt addresses",
				fixture: `{
					"name":"selected",
					"file":"selected.bin.gz",
					"resume_target_count":2,
					"interrupt_ids":["first","second"],
					"interrupt_addresses":["only"]
				}`,
				wantErr: `fixture "selected" resume target count 2 exceeds interrupt addresses length 1`,
			},
			{
				name: "default resume target count exceeds interrupt addresses",
				fixture: `{
					"name":"selected",
					"file":"selected.bin.gz",
					"interrupt_ids":["only"]
				}`,
				wantErr: `fixture "selected" resume target count 1 exceeds interrupt addresses length 0`,
			},
			{
				name: "negative expected interrupts",
				fixture: `{
					"name":"selected",
					"file":"selected.bin.gz",
					"cancel":true,
					"expected_interrupts":-1
				}`,
				wantErr: `fixture "selected" has invalid expected interrupts -1`,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				var readFixtureCalls, newResumerCalls int
				runner := &recordingResumer{}
				dependencies := defaultRunDependencies()
				dependencies.readFile = func(string) ([]byte, error) {
					return []byte(`{"fixtures":[` + tt.fixture + `]}`), nil
				}
				dependencies.readFixture = func(string) ([]byte, error) {
					readFixtureCalls++
					return []byte("checkpoint"), nil
				}
				dependencies.newResumer = func(context.Context, fixture, []byte) (
					checkpointResumer, error) {
					newResumerCalls++
					return runner, nil
				}

				err := run([]string{
					"-fixture-dir", "fixtures",
					"-fixture", "selected",
				}, &bytes.Buffer{}, dependencies)
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				if readFixtureCalls != 0 || newResumerCalls != 0 ||
					runner.resumeCalls != 0 || runner.resumeWithParamsCalls != 0 {
					t.Fatalf(
						"calls: readFixture=%d newResumer=%d Resume=%d ResumeWithParams=%d, want all zero",
						readFixtureCalls, newResumerCalls, runner.resumeCalls,
						runner.resumeWithParamsCalls)
				}
			})
		}
	})

	fixtureManifest := []byte(`{
		"fixtures":[{
			"name":"selected",
			"file":"selected.bin.gz",
			"cancel":true
		}]
	}`)
	newDependencies := func(runner checkpointResumer) runDependencies {
		dependencies := defaultRunDependencies()
		dependencies.readFile = func(string) ([]byte, error) {
			return fixtureManifest, nil
		}
		dependencies.readFixture = func(string) ([]byte, error) {
			return []byte("checkpoint"), nil
		}
		dependencies.newResumer = func(context.Context, fixture, []byte) (
			checkpointResumer, error) {
			return runner, nil
		}
		return dependencies
	}
	args := []string{"-fixture-dir", "fixtures", "-fixture", "selected"}

	t.Run("returns fixture read failure", func(t *testing.T) {
		dependencies := newDependencies(&recordingResumer{})
		dependencies.readFixture = func(string) ([]byte, error) {
			return nil, readErr
		}
		err := run(args, &bytes.Buffer{}, dependencies)
		if !errors.Is(err, readErr) {
			t.Fatalf("error = %v, want %v", err, readErr)
		}
	})

	t.Run("returns resumer construction failure", func(t *testing.T) {
		dependencies := newDependencies(nil)
		dependencies.newResumer = func(context.Context, fixture, []byte) (
			checkpointResumer, error) {
			return nil, readErr
		}
		err := run(args, &bytes.Buffer{}, dependencies)
		if !errors.Is(err, readErr) {
			t.Fatalf("error = %v, want %v", err, readErr)
		}
	})

	t.Run("returns resume failure", func(t *testing.T) {
		err := run(args, &bytes.Buffer{}, newDependencies(
			&recordingResumer{err: readErr}))
		if !errors.Is(err, readErr) {
			t.Fatalf("error = %v, want %v", err, readErr)
		}
	})

	t.Run("rejects empty event stream", func(t *testing.T) {
		iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
		generator.Close()
		err := run(args, &bytes.Buffer{}, newDependencies(
			&recordingResumer{iter: iter}))
		if err == nil || err.Error() != `fixture "selected" resume produced no events` {
			t.Fatalf("error = %v, want empty stream failure", err)
		}
	})

	t.Run("returns event failure", func(t *testing.T) {
		iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
		generator.Send(&adk.AgentEvent{Err: readErr})
		generator.Close()
		err := run(args, &bytes.Buffer{}, newDependencies(
			&recordingResumer{iter: iter}))
		if err == nil || err.Error() != "resume events failed: "+readErr.Error() {
			t.Fatalf("error = %v, want event failure", err)
		}
	})

	t.Run("validates expected interrupt metadata", func(t *testing.T) {
		manifestWithExpectedInterrupt := []byte(`{
			"fixtures":[{
				"name":"selected",
				"file":"selected.bin.gz",
				"cancel":true,
				"expected_interrupts":1
			}]
		}`)
		iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
		generator.Send(&adk.AgentEvent{})
		generator.Close()
		dependencies := newDependencies(&recordingResumer{iter: iter})
		dependencies.readFile = func(string) ([]byte, error) {
			return manifestWithExpectedInterrupt, nil
		}
		err := run(args, &bytes.Buffer{}, dependencies)
		wantErr := `fixture "selected" metadata declares 1 remaining interrupts, but has 0 addresses`
		if err == nil || err.Error() != wantErr {
			t.Fatalf("error = %v, want metadata mismatch", err)
		}
	})
}
