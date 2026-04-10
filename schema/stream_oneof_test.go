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

package schema_test

import (
	"errors"
	"io"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

func recvAll(t *testing.T, sr *schema.StreamReader[string]) ([]string, []error) {
	t.Helper()
	var vals []string
	var errs []error
	for {
		v, err := sr.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		vals = append(vals, v)
	}
	return vals, errs
}

func makeStream(items []string, opts ...schema.ConvertOption) *schema.StreamReader[string] {
	return schema.StreamReaderWithConvert(
		schema.StreamReaderFromArray(items),
		func(s string) (string, error) { return s, nil },
		opts...,
	)
}

func TestWithOnEOF_PassThroughEOF(t *testing.T) {
	items := []string{"a", "b", "c", "d"}
	sr := makeStream(items, schema.WithOnEOF(func() (any, error) {
		return nil, io.EOF
	}))
	defer sr.Close()

	vals, errs := recvAll(t, sr)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
	if len(vals) != 4 {
		t.Fatalf("expected 4 values, got %d: %v", len(vals), vals)
	}
	for i, want := range items {
		if vals[i] != want {
			t.Errorf("vals[%d] = %q, want %q", i, vals[i], want)
		}
	}
}

func TestWithOnEOF_InjectError(t *testing.T) {
	items := []string{"a", "b", "c", "d"}
	customErr := errors.New("validation failed")
	sr := makeStream(items, schema.WithOnEOF(func() (any, error) {
		return nil, customErr
	}))
	defer sr.Close()

	var vals []string
	var gotCustomErr bool
	for {
		v, err := sr.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if errors.Is(err, customErr) {
				gotCustomErr = true
				continue
			}
			t.Fatalf("unexpected error: %v", err)
		}
		vals = append(vals, v)
	}

	if len(vals) != 4 {
		t.Fatalf("expected 4 values, got %d: %v", len(vals), vals)
	}
	if !gotCustomErr {
		t.Fatalf("expected custom error from onEOF, did not receive it")
	}
}

func TestWithOnEOF_InjectValue(t *testing.T) {
	items := []string{"a", "b", "c", "d"}
	sr := makeStream(items, schema.WithOnEOF(func() (any, error) {
		return "extra", nil
	}))
	defer sr.Close()

	var vals []string
	for {
		v, err := sr.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		vals = append(vals, v)
	}

	if len(vals) != 5 {
		t.Fatalf("expected 5 values, got %d: %v", len(vals), vals)
	}
	if vals[4] != "extra" {
		t.Errorf("vals[4] = %q, want %q", vals[4], "extra")
	}
}

func TestWithOnEOF_BlockingCallback(t *testing.T) {
	sr, sw := schema.Pipe[string](0)

	unblock := make(chan struct{})
	converted := schema.StreamReaderWithConvert(sr,
		func(s string) (string, error) { return s, nil },
		schema.WithOnEOF(func() (any, error) {
			<-unblock
			return "after-block", nil
		}),
	)
	defer converted.Close()

	go func() {
		sw.Send("x", nil)
		sw.Close()
	}()

	v, err := converted.Recv()
	if err != nil {
		t.Fatalf("first Recv error: %v", err)
	}
	if v != "x" {
		t.Fatalf("first Recv = %q, want %q", v, "x")
	}

	done := make(chan struct{})
	var recvVal string
	var recvErr error
	go func() {
		recvVal, recvErr = converted.Recv()
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("Recv returned before unblock signal")
	case <-time.After(50 * time.Millisecond):
	}

	close(unblock)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Recv did not return after unblock signal")
	}

	if recvErr != nil {
		t.Fatalf("second Recv error: %v", recvErr)
	}
	if recvVal != "after-block" {
		t.Errorf("second Recv = %q, want %q", recvVal, "after-block")
	}

	v3, err3 := converted.Recv()
	if !errors.Is(err3, io.EOF) {
		t.Fatalf("third Recv: got (%q, %v), want EOF", v3, err3)
	}
}

func TestWithOnEOF_EmptyStream(t *testing.T) {
	customErr := errors.New("empty stream error")
	sr := makeStream(nil, schema.WithOnEOF(func() (any, error) {
		return nil, customErr
	}))
	defer sr.Close()

	v, err := sr.Recv()
	if !errors.Is(err, customErr) {
		t.Fatalf("first Recv: got (%q, %v), want customErr", v, err)
	}

	v2, err2 := sr.Recv()
	if !errors.Is(err2, io.EOF) {
		t.Fatalf("second Recv: got (%q, %v), want EOF", v2, err2)
	}
}

func TestWithOnEOF_WithErrWrapper_ErrorPath(t *testing.T) {
	sr, sw := schema.Pipe[string](0)

	streamErr := errors.New("stream error")
	onEOFCalled := false

	converted := schema.StreamReaderWithConvert(sr,
		func(s string) (string, error) { return s, nil },
		schema.WithErrWrapper(func(err error) error {
			return err
		}),
		schema.WithOnEOF(func() (any, error) {
			onEOFCalled = true
			return nil, errors.New("should not happen")
		}),
	)
	defer converted.Close()

	go func() {
		sw.Send("a", nil)
		sw.Send("", streamErr)
		sw.Close()
	}()

	v, err := converted.Recv()
	if err != nil {
		t.Fatalf("first Recv error: %v", err)
	}
	if v != "a" {
		t.Fatalf("first Recv = %q, want %q", v, "a")
	}

	_, err = converted.Recv()
	if !errors.Is(err, streamErr) {
		t.Fatalf("second Recv: got %v, want streamErr", err)
	}

	if onEOFCalled {
		t.Fatal("onEOF should not have been called when stream errored")
	}
}

func TestWithOnEOF_WithErrWrapper_EOFPath(t *testing.T) {
	items := []string{"a", "b", "c"}
	errWrapperCalled := false

	sr := schema.StreamReaderWithConvert(
		schema.StreamReaderFromArray(items),
		func(s string) (string, error) { return s, nil },
		schema.WithErrWrapper(func(err error) error {
			errWrapperCalled = true
			return err
		}),
		schema.WithOnEOF(func() (any, error) {
			return "oneof-val", nil
		}),
	)
	defer sr.Close()

	var vals []string
	for {
		v, err := sr.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		vals = append(vals, v)
	}

	if len(vals) != 4 {
		t.Fatalf("expected 4 values, got %d: %v", len(vals), vals)
	}
	if vals[3] != "oneof-val" {
		t.Errorf("vals[3] = %q, want %q", vals[3], "oneof-val")
	}
	if errWrapperCalled {
		t.Fatal("errWrapper should not have been called for clean stream")
	}
}

func TestWithOnEOF_MultipleRecvAfterEOF(t *testing.T) {
	items := []string{"a"}
	customErr := errors.New("oneof error")

	sr := makeStream(items, schema.WithOnEOF(func() (any, error) {
		return nil, customErr
	}))
	defer sr.Close()

	v, err := sr.Recv()
	if err != nil {
		t.Fatalf("first Recv error: %v", err)
	}
	if v != "a" {
		t.Fatalf("first Recv = %q, want %q", v, "a")
	}

	_, err = sr.Recv()
	if !errors.Is(err, customErr) {
		t.Fatalf("second Recv: got %v, want customErr", err)
	}

	for i := 0; i < 5; i++ {
		_, err = sr.Recv()
		if !errors.Is(err, io.EOF) {
			t.Fatalf("Recv #%d after onEOF: got %v, want io.EOF", i+3, err)
		}
	}
}
