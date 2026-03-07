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

package agentmd

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// --- test helpers ---

type mockModel struct {
	lastInput []*schema.Message
}

func (m *mockModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.lastInput = input
	return &schema.Message{Role: schema.Assistant, Content: "ok"}, nil
}

func (m *mockModel) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.lastInput = input
	return nil, nil
}

type memBackend struct {
	files map[string]string
}

func newMemBackend() *memBackend {
	return &memBackend{files: make(map[string]string)}
}

func (b *memBackend) set(path string, content string) {
	b.files[path] = content
}

func (b *memBackend) Read(_ context.Context, req *ReadRequest) (string, error) {
	content, ok := b.files[req.FilePath]
	if !ok {
		return "", fmt.Errorf("file not found: %s", req.FilePath)
	}
	return content, nil
}

// --- tests ---

func TestNew_Validation(t *testing.T) {
	ctx := context.Background()
	b := newMemBackend()

	_, err := New(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil config")
	}

	_, err = New(ctx, &Config{})
	if err == nil {
		t.Fatal("expected error for empty config")
	}

	_, err = New(ctx, &Config{Backend: b, AgentFiles: []string{"/test.md"}, AllAgentMDDocsMaxBytes: -1})
	if err == nil {
		t.Fatal("expected error for negative max bytes")
	}

	_, err = New(ctx, &Config{AgentFiles: []string{"/test.md"}})
	if err == nil {
		t.Fatal("expected error for nil backend")
	}
}

func TestMiddleware_BasicInjection(t *testing.T) {
	b := newMemBackend()
	b.set("/agent.md", "You are a helpful assistant.")

	ctx := context.Background()
	mw, err := New(ctx, &Config{Backend: b, AgentFiles: []string{"/agent.md"}})
	if err != nil {
		t.Fatal(err)
	}

	mock := &mockModel{}
	wrapped, err := mw.WrapModel(ctx, mock, nil)
	if err != nil {
		t.Fatal(err)
	}

	userMsg := &schema.Message{Role: schema.User, Content: "hello"}
	if _, err = wrapped.Generate(ctx, []*schema.Message{userMsg}); err != nil {
		t.Fatal(err)
	}

	if len(mock.lastInput) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(mock.lastInput))
	}
	if mock.lastInput[0].Role != schema.User {
		t.Fatalf("expected first message role User, got %s", mock.lastInput[0].Role)
	}
	if !strings.Contains(mock.lastInput[0].Content, "You are a helpful assistant.") {
		t.Fatalf("expected agent.md content in first message, got %q", mock.lastInput[0].Content)
	}
	if !strings.Contains(mock.lastInput[0].Content, "<system-reminder>") {
		t.Fatalf("expected system-reminder tag, got %q", mock.lastInput[0].Content)
	}
	if mock.lastInput[1].Content != "hello" {
		t.Fatalf("expected original message preserved, got %q", mock.lastInput[1].Content)
	}
}

func TestMiddleware_MultipleFiles(t *testing.T) {
	b := newMemBackend()
	b.set("/a.md", "instruction A")
	b.set("/b.md", "instruction B")

	ctx := context.Background()
	mw, err := New(ctx, &Config{Backend: b, AgentFiles: []string{"/a.md", "/b.md"}})
	if err != nil {
		t.Fatal(err)
	}

	mock := &mockModel{}
	wrapped, err := mw.WrapModel(ctx, mock, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = wrapped.Generate(ctx, []*schema.Message{{Role: schema.User, Content: "hi"}}); err != nil {
		t.Fatal(err)
	}

	content := mock.lastInput[0].Content
	idxA := strings.Index(content, "instruction A")
	idxB := strings.Index(content, "instruction B")
	if idxA < 0 || idxB < 0 {
		t.Fatalf("both files should be included, content: %q", content)
	}
	if idxA >= idxB {
		t.Fatal("file A should appear before file B")
	}
}

func TestMiddleware_ImportResolution(t *testing.T) {
	b := newMemBackend()
	b.set("/project/agent.md", "main instructions\n@sub/rules.md\nend")
	b.set("/project/sub/rules.md", "imported rule")

	ctx := context.Background()
	mw, err := New(ctx, &Config{Backend: b, AgentFiles: []string{"/project/agent.md"}})
	if err != nil {
		t.Fatal(err)
	}

	mock := &mockModel{}
	wrapped, err := mw.WrapModel(ctx, mock, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = wrapped.Generate(ctx, []*schema.Message{{Role: schema.User, Content: "hi"}}); err != nil {
		t.Fatal(err)
	}

	content := mock.lastInput[0].Content
	// Original text should be preserved with @path intact.
	if !strings.Contains(content, "main instructions") {
		t.Fatalf("should contain original text, got %q", content)
	}
	if !strings.Contains(content, "@sub/rules.md") {
		t.Fatalf("@import reference should be preserved in original text, got %q", content)
	}
	if !strings.Contains(content, "end") {
		t.Fatalf("should contain original trailing text, got %q", content)
	}
	// Imported file should appear as a separate section.
	if !strings.Contains(content, "Contents of /project/sub/rules.md") {
		t.Fatalf("imported file should have its own section, got %q", content)
	}
	if !strings.Contains(content, "imported rule") {
		t.Fatalf("imported file content should be present, got %q", content)
	}
}

func TestMiddleware_RecursiveImport(t *testing.T) {
	b := newMemBackend()
	b.set("/a.md", "top\n@/b.md")
	b.set("/b.md", "middle\n@/c.md")
	b.set("/c.md", "leaf content")

	ctx := context.Background()
	mw, err := New(ctx, &Config{Backend: b, AgentFiles: []string{"/a.md"}})
	if err != nil {
		t.Fatal(err)
	}

	mock := &mockModel{}
	wrapped, err := mw.WrapModel(ctx, mock, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = wrapped.Generate(ctx, []*schema.Message{{Role: schema.User, Content: "hi"}}); err != nil {
		t.Fatal(err)
	}

	content := mock.lastInput[0].Content
	// All three files should appear as separate sections.
	for _, section := range []string{"Contents of /a.md", "Contents of /b.md", "Contents of /c.md"} {
		if !strings.Contains(content, section) {
			t.Fatalf("expected section %q in content, got %q", section, content)
		}
	}
	for _, text := range []string{"top", "middle", "leaf content"} {
		if !strings.Contains(content, text) {
			t.Fatalf("expected %q in content, got %q", text, content)
		}
	}
	// Sections should appear in order: a, b, c.
	idxA := strings.Index(content, "Contents of /a.md")
	idxB := strings.Index(content, "Contents of /b.md")
	idxC := strings.Index(content, "Contents of /c.md")
	if !(idxA < idxB && idxB < idxC) {
		t.Fatalf("sections should appear in order a < b < c, got a=%d b=%d c=%d", idxA, idxB, idxC)
	}
}

func TestMiddleware_MaxImportDepth(t *testing.T) {
	b := newMemBackend()
	for i := 0; i < 7; i++ {
		var content string
		if i < 6 {
			content = fmt.Sprintf("level %d\n@/level%d.md", i, i+1)
		} else {
			content = fmt.Sprintf("level %d", i)
		}
		b.set(fmt.Sprintf("/level%d.md", i), content)
	}

	ctx := context.Background()
	mw, err := New(ctx, &Config{Backend: b, AgentFiles: []string{"/level0.md"}})
	if err != nil {
		t.Fatal(err)
	}

	mock := &mockModel{}
	wrapped, err := mw.WrapModel(ctx, mock, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Import failure at depth > 5 is logged, not returned as error.
	_, err = wrapped.Generate(ctx, []*schema.Message{{Role: schema.User, Content: "hi"}})
	if err != nil {
		t.Fatalf("expected no error (depth exceeded is logged), got %v", err)
	}
	// Levels 0-5 should be present as sections; level 6 fails silently.
	content := mock.lastInput[0].Content
	for i := 0; i <= 5; i++ {
		want := fmt.Sprintf("Contents of /level%d.md", i)
		if !strings.Contains(content, want) {
			t.Fatalf("expected %q in content, got %q", want, content)
		}
	}
	if strings.Contains(content, "Contents of /level6.md") {
		t.Fatalf("level6 should not be present (depth exceeded), got %q", content)
	}
}

func TestMiddleware_CircularImport(t *testing.T) {
	b := newMemBackend()
	b.set("/a.md", "@/b.md")
	b.set("/b.md", "@/a.md")

	ctx := context.Background()
	mw, err := New(ctx, &Config{Backend: b, AgentFiles: []string{"/a.md"}})
	if err != nil {
		t.Fatal(err)
	}

	mock := &mockModel{}
	wrapped, err := mw.WrapModel(ctx, mock, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Circular import failure is logged, not returned as error.
	_, err = wrapped.Generate(ctx, []*schema.Message{{Role: schema.User, Content: "hi"}})
	if err != nil {
		t.Fatalf("expected no error (circular import is logged), got %v", err)
	}
	// /a.md and /b.md should both be present; the circular ref from b->a is skipped.
	content := mock.lastInput[0].Content
	if !strings.Contains(content, "Contents of /a.md") {
		t.Fatalf("expected /a.md section, got %q", content)
	}
	if !strings.Contains(content, "Contents of /b.md") {
		t.Fatalf("expected /b.md section, got %q", content)
	}
}

func TestMiddleware_MaxBytesLimit(t *testing.T) {
	b := newMemBackend()
	b.set("/a.md", "AAAA") // 4 bytes
	b.set("/b.md", "BBBB") // 4 bytes

	ctx := context.Background()
	mw, err := New(ctx, &Config{
		Backend:                b,
		AgentFiles:             []string{"/a.md", "/b.md"},
		AllAgentMDDocsMaxBytes: 5, // file a (4) fits, file b (4) would exceed
	})
	if err != nil {
		t.Fatal(err)
	}

	mock := &mockModel{}
	wrapped, err := mw.WrapModel(ctx, mock, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = wrapped.Generate(ctx, []*schema.Message{{Role: schema.User, Content: "hi"}}); err != nil {
		t.Fatal(err)
	}

	content := mock.lastInput[0].Content
	if !strings.Contains(content, "AAAA") {
		t.Fatal("first file should be included")
	}
	if strings.Contains(content, "BBBB") {
		t.Fatal("second file should be excluded due to max bytes")
	}
}

func TestMiddleware_NotPersistedInState(t *testing.T) {
	b := newMemBackend()
	b.set("/agent.md", "agent instructions")

	ctx := context.Background()
	mw, err := New(ctx, &Config{Backend: b, AgentFiles: []string{"/agent.md"}})
	if err != nil {
		t.Fatal(err)
	}

	mock := &mockModel{}
	wrapped, err := mw.WrapModel(ctx, mock, nil)
	if err != nil {
		t.Fatal(err)
	}

	originalMsgs := []*schema.Message{{Role: schema.User, Content: "hello"}}
	if _, err = wrapped.Generate(ctx, originalMsgs); err != nil {
		t.Fatal(err)
	}

	if len(originalMsgs) != 1 {
		t.Fatalf("original messages should not be modified, got %d messages", len(originalMsgs))
	}
	if originalMsgs[0].Content != "hello" {
		t.Fatalf("original message should be unchanged, got %q", originalMsgs[0].Content)
	}
	if len(mock.lastInput) != 2 {
		t.Fatalf("model should receive 2 messages, got %d", len(mock.lastInput))
	}
}

func TestMiddleware_AbsoluteImportPath(t *testing.T) {
	b := newMemBackend()
	b.set("/project/main.md", "start\n@/shared/imported.md\nend")
	b.set("/shared/imported.md", "absolute import content")

	ctx := context.Background()
	mw, err := New(ctx, &Config{Backend: b, AgentFiles: []string{"/project/main.md"}})
	if err != nil {
		t.Fatal(err)
	}

	mock := &mockModel{}
	wrapped, err := mw.WrapModel(ctx, mock, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = wrapped.Generate(ctx, []*schema.Message{{Role: schema.User, Content: "hi"}}); err != nil {
		t.Fatal(err)
	}

	content := mock.lastInput[0].Content
	// @path preserved in original text.
	if !strings.Contains(content, "@/shared/imported.md") {
		t.Fatalf("@import reference should be preserved, got %q", content)
	}
	// Imported content in separate section.
	if !strings.Contains(content, "Contents of /shared/imported.md") {
		t.Fatalf("expected separate section for imported file, got %q", content)
	}
	if !strings.Contains(content, "absolute import content") {
		t.Fatalf("expected absolute import content, got %q", content)
	}
}

func TestMiddleware_ImportAsSeparateSection(t *testing.T) {
	b := newMemBackend()
	b.set("/project/agent.md", "Please read @sub/rules.md and also @sub/style.md for guidance.")
	b.set("/project/sub/rules.md", "RULE_CONTENT")
	b.set("/project/sub/style.md", "STYLE_CONTENT")

	ctx := context.Background()
	mw, err := New(ctx, &Config{Backend: b, AgentFiles: []string{"/project/agent.md"}})
	if err != nil {
		t.Fatal(err)
	}

	mock := &mockModel{}
	wrapped, err := mw.WrapModel(ctx, mock, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = wrapped.Generate(ctx, []*schema.Message{{Role: schema.User, Content: "hi"}}); err != nil {
		t.Fatal(err)
	}

	content := mock.lastInput[0].Content
	// Original text preserved with @paths intact.
	if !strings.Contains(content, "Please read @sub/rules.md and also @sub/style.md for guidance.") {
		t.Fatalf("original text with @paths should be preserved, got %q", content)
	}
	// Imported files appear as separate sections.
	if !strings.Contains(content, "Contents of /project/sub/rules.md") {
		t.Fatalf("expected rules.md section, got %q", content)
	}
	if !strings.Contains(content, "RULE_CONTENT") {
		t.Fatalf("expected imported rule content, got %q", content)
	}
	if !strings.Contains(content, "Contents of /project/sub/style.md") {
		t.Fatalf("expected style.md section, got %q", content)
	}
	if !strings.Contains(content, "STYLE_CONTENT") {
		t.Fatalf("expected imported style content, got %q", content)
	}

	// Sections should be ordered: agent.md, rules.md, style.md.
	idxAgent := strings.Index(content, "Contents of /project/agent.md")
	idxRules := strings.Index(content, "Contents of /project/sub/rules.md")
	idxStyle := strings.Index(content, "Contents of /project/sub/style.md")
	if !(idxAgent < idxRules && idxRules < idxStyle) {
		t.Fatalf("sections should appear in order agent < rules < style, got agent=%d rules=%d style=%d", idxAgent, idxRules, idxStyle)
	}
}

// --- loader-specific tests ---

func TestLoader_NoImportsPassthrough(t *testing.T) {
	// Content without any @path should be returned as-is in its section.
	b := newMemBackend()
	b.set("/agent.md", "plain text without imports\nline two")

	l := newLoader(b, []string{"/agent.md"}, 0)
	content, err := l.load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "plain text without imports") {
		t.Fatalf("expected plain content, got %q", content)
	}
	if !strings.Contains(content, "line two") {
		t.Fatalf("expected second line, got %q", content)
	}
}

func TestLoader_ImportAsSeparateSection(t *testing.T) {
	// @path in the middle of a sentence should be preserved; imported file is a separate section.
	b := newMemBackend()
	b.set("/doc.md", "before @/snippet.md after")
	b.set("/snippet.md", "INJECTED")

	l := newLoader(b, []string{"/doc.md"}, 0)
	content, err := l.load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Original text preserved.
	if !strings.Contains(content, "before @/snippet.md after") {
		t.Fatalf("original text should be preserved with @path, got %q", content)
	}
	// Imported file in separate section.
	if !strings.Contains(content, "Contents of /snippet.md") {
		t.Fatalf("expected separate section for snippet.md, got %q", content)
	}
	if !strings.Contains(content, "INJECTED") {
		t.Fatalf("expected imported content, got %q", content)
	}
}

func TestLoader_MultipleImportsSameLine(t *testing.T) {
	// Multiple @path on one line should each get a separate section.
	b := newMemBackend()
	b.set("/doc.md", "see @/a.txt and @/b.txt here")
	b.set("/a.txt", "AAA")
	b.set("/b.txt", "BBB")

	l := newLoader(b, []string{"/doc.md"}, 0)
	content, err := l.load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Original text preserved.
	if !strings.Contains(content, "see @/a.txt and @/b.txt here") {
		t.Fatalf("original text should be preserved, got %q", content)
	}
	// Each imported file has its own section.
	if !strings.Contains(content, "Contents of /a.txt") {
		t.Fatalf("expected section for a.txt, got %q", content)
	}
	if !strings.Contains(content, "AAA") {
		t.Fatalf("expected a.txt content, got %q", content)
	}
	if !strings.Contains(content, "Contents of /b.txt") {
		t.Fatalf("expected section for b.txt, got %q", content)
	}
	if !strings.Contains(content, "BBB") {
		t.Fatalf("expected b.txt content, got %q", content)
	}
}

func TestLoader_SameFileTwiceOnSameLine(t *testing.T) {
	// The same file referenced twice should appear only once as a section (deduped).
	b := newMemBackend()
	b.set("/doc.md", "@/shared.md and @/shared.md again")
	b.set("/shared.md", "SHARED")

	l := newLoader(b, []string{"/doc.md"}, 0)
	content, err := l.load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Original text preserved.
	if !strings.Contains(content, "@/shared.md and @/shared.md again") {
		t.Fatalf("original text should be preserved, got %q", content)
	}
	// shared.md content should appear only once (deduped).
	count := strings.Count(content, "Contents of /shared.md")
	if count != 1 {
		t.Fatalf("expected shared.md section to appear once (deduped), got %d in %q", count, content)
	}
}

func TestLoader_ImportFileNotFound(t *testing.T) {
	b := newMemBackend()
	b.set("/doc.md", "load @/missing.md please")

	l := newLoader(b, []string{"/doc.md"}, 0)
	content, err := l.load(context.Background())
	if err != nil {
		t.Fatalf("expected no error (missing import is logged), got %v", err)
	}
	// Original text preserved; missing file simply has no section.
	if !strings.Contains(content, "load @/missing.md please") {
		t.Fatalf("expected original text preserved, got %q", content)
	}
	if strings.Contains(content, "Contents of /missing.md") {
		t.Fatalf("missing file should not have a section, got %q", content)
	}
}

func TestLoader_RelativePathResolution(t *testing.T) {
	// Relative path should resolve relative to the host file's directory.
	b := newMemBackend()
	b.set("/a/b/host.md", "ref @../c/target.md done")
	b.set("/a/c/target.md", "TARGET")

	l := newLoader(b, []string{"/a/b/host.md"}, 0)
	content, err := l.load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Original text preserved.
	if !strings.Contains(content, "ref @../c/target.md done") {
		t.Fatalf("original text should be preserved, got %q", content)
	}
	// Imported file as separate section.
	if !strings.Contains(content, "Contents of /a/c/target.md") {
		t.Fatalf("expected section for target.md, got %q", content)
	}
	if !strings.Contains(content, "TARGET") {
		t.Fatalf("expected imported content, got %q", content)
	}
}

func TestLoader_NestedRelativeImport(t *testing.T) {
	// File A imports B via relative path, B imports C via relative path.
	// All three should appear as separate sections.
	b := newMemBackend()
	b.set("/root/main.md", "start @sub/mid.md end")
	b.set("/root/sub/mid.md", "mid @deep/leaf.md mid_end")
	b.set("/root/sub/deep/leaf.md", "LEAF")

	l := newLoader(b, []string{"/root/main.md"}, 0)
	content, err := l.load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, section := range []string{"Contents of /root/main.md", "Contents of /root/sub/mid.md", "Contents of /root/sub/deep/leaf.md"} {
		if !strings.Contains(content, section) {
			t.Fatalf("expected section %q, got %q", section, content)
		}
	}
	if !strings.Contains(content, "LEAF") {
		t.Fatalf("expected leaf content, got %q", content)
	}
}

func TestLoader_TransitiveImport(t *testing.T) {
	// Imported file itself contains @imports; all should appear as separate sections.
	b := newMemBackend()
	b.set("/main.md", "header @/mid.md footer")
	b.set("/mid.md", "mid-start @/leaf.md mid-end")
	b.set("/leaf.md", "LEAF_VALUE")

	l := newLoader(b, []string{"/main.md"}, 0)
	content, err := l.load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, section := range []string{"Contents of /main.md", "Contents of /mid.md", "Contents of /leaf.md"} {
		if !strings.Contains(content, section) {
			t.Fatalf("expected section %q, got %q", section, content)
		}
	}
	if !strings.Contains(content, "LEAF_VALUE") {
		t.Fatalf("expected leaf value, got %q", content)
	}
}

func TestLoader_EmptyFile(t *testing.T) {
	b := newMemBackend()
	b.set("/empty.md", "")

	l := newLoader(b, []string{"/empty.md"}, 0)
	content, err := l.load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Empty file content is still wrapped in system-reminder with the file path header.
	if !strings.Contains(content, "/empty.md") {
		t.Fatalf("expected file path in output, got %q", content)
	}
}

func TestLoader_MaxBytesFirstFileFull(t *testing.T) {
	// Even if the first file alone exceeds maxBytes, it should still be loaded in full.
	b := newMemBackend()
	b.set("/big.md", "ABCDEFGHIJ") // 10 bytes

	l := newLoader(b, []string{"/big.md"}, 3)
	content, err := l.load(context.Background()) // maxBytes=3, but first file always loads
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "ABCDEFGHIJ") {
		t.Fatalf("first file should always load in full, got %q", content)
	}
}

func TestLoader_CircularImportInline(t *testing.T) {
	// Circular reference via @import should be detected, logged, and skipped.
	b := newMemBackend()
	b.set("/a.md", "text @/b.md more")
	b.set("/b.md", "ref @/a.md back")

	l := newLoader(b, []string{"/a.md"}, 0)
	content, err := l.load(context.Background())
	if err != nil {
		t.Fatalf("expected no error (circular import is logged), got %v", err)
	}
	// Both a and b should have sections; circular back-reference a from b is skipped.
	if !strings.Contains(content, "Contents of /a.md") {
		t.Fatalf("expected /a.md section, got %q", content)
	}
	if !strings.Contains(content, "Contents of /b.md") {
		t.Fatalf("expected /b.md section, got %q", content)
	}
}

func TestLoader_MaxDepthInline(t *testing.T) {
	// Deep chain via @import should be logged at depth > 5, not returned as error.
	b := newMemBackend()
	for i := 0; i < 7; i++ {
		var content string
		if i < 6 {
			content = fmt.Sprintf("level%d @/level%d.md tail", i, i+1)
		} else {
			content = fmt.Sprintf("level%d", i)
		}
		b.set(fmt.Sprintf("/level%d.md", i), content)
	}

	l := newLoader(b, []string{"/level0.md"}, 0)
	content, err := l.load(context.Background())
	if err != nil {
		t.Fatalf("expected no error (depth exceeded is logged), got %v", err)
	}
	// Levels 0-5 should have sections.
	for i := 0; i <= 5; i++ {
		want := fmt.Sprintf("Contents of /level%d.md", i)
		if !strings.Contains(content, want) {
			t.Fatalf("expected %q in content, got %q", want, content)
		}
	}
	// Level 6 should not be present.
	if strings.Contains(content, "Contents of /level6.md") {
		t.Fatalf("level6 should not be present (depth exceeded), got %q", content)
	}
}

func TestLoader_DiamondDependency(t *testing.T) {
	// A imports B and D; B imports C; D also imports C.
	// C should appear only once (deduped across the whole load).
	b := newMemBackend()
	b.set("/a.md", "start @/b.md middle @/d.md end")
	b.set("/b.md", "B(@/c.md)")
	b.set("/d.md", "D(@/c.md)")
	b.set("/c.md", "SHARED")

	l := newLoader(b, []string{"/a.md"}, 0)
	content, err := l.load(context.Background())
	if err != nil {
		t.Fatalf("diamond dependency should not be circular, got error: %v", err)
	}

	// C should appear only once as a section (deduped).
	count := strings.Count(content, "Contents of /c.md")
	if count != 1 {
		t.Fatalf("expected /c.md section once (deduped), got %d in %q", count, content)
	}
	// All files should have sections.
	for _, section := range []string{"Contents of /a.md", "Contents of /b.md", "Contents of /c.md", "Contents of /d.md"} {
		if !strings.Contains(content, section) {
			t.Fatalf("expected section %q, got %q", section, content)
		}
	}
}

func TestLoader_AtSignInNormalText(t *testing.T) {
	// Bare @word without "/" or file extension should not trigger import.
	// Email-like patterns (@example.com) with non-allowed extensions should also be ignored.
	b := newMemBackend()
	b.set("/agent.md", "contact me @ anytime or @  spaces and @someone mentioned and user@example.com and @company.org")

	l := newLoader(b, []string{"/agent.md"}, 0)
	content, err := l.load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "contact me @ anytime") {
		t.Fatalf("bare @ should not trigger import, got %q", content)
	}
	if !strings.Contains(content, "@someone mentioned") {
		t.Fatalf("@someone without / or extension should not trigger import, got %q", content)
	}
	if !strings.Contains(content, "@example.com") {
		t.Fatalf("email-like @example.com should not trigger import, got %q", content)
	}
	if !strings.Contains(content, "@company.org") {
		t.Fatalf("email-like @company.org should not trigger import, got %q", content)
	}
}

func TestMiddleware_Stream(t *testing.T) {
	b := newMemBackend()
	b.set("/agent.md", "stream test")

	ctx := context.Background()
	mw, err := New(ctx, &Config{Backend: b, AgentFiles: []string{"/agent.md"}})
	if err != nil {
		t.Fatal(err)
	}

	mock := &mockModel{}
	wrapped, err := mw.WrapModel(ctx, mock, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, _ = wrapped.Stream(ctx, []*schema.Message{{Role: schema.User, Content: "hi"}})

	if len(mock.lastInput) != 2 {
		t.Fatalf("expected 2 messages for stream, got %d", len(mock.lastInput))
	}
	if !strings.Contains(mock.lastInput[0].Content, "stream test") {
		t.Fatalf("expected agent.md content in stream input, got %q", mock.lastInput[0].Content)
	}
}

func TestLoader_MaxBytesWithImports(t *testing.T) {
	// Two top-level files that both import the same shared file.
	// Budget should account for imported file bytes.
	b := newMemBackend()
	b.set("/a.md", "A(@/shared.md)")
	b.set("/b.md", "B(@/shared.md)")
	b.set("/shared.md", strings.Repeat("X", 100)) // 100 bytes

	l := newLoader(b, []string{"/a.md", "/b.md"}, 120)
	// /a.md = 14 bytes + /shared.md = 100 bytes => 114 total after /a.md.
	// Budget = 120: /b.md (14 bytes) would push to 128, exceeding budget.
	content, err := l.load(context.Background())
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}

	// /a.md and its import should be included.
	if !strings.Contains(content, strings.Repeat("X", 100)) {
		t.Fatal("expected /a.md with shared content to be included")
	}

	// /b.md should be excluded because totalBytes exceeded budget after loading /a.md.
	if strings.Contains(content, "B(") {
		t.Fatalf("expected /b.md to be excluded due to budget, got %q", content)
	}
}

func TestNew_Validation_EmptyAgentFiles(t *testing.T) {
	ctx := context.Background()
	b := newMemBackend()

	_, err := New(ctx, &Config{Backend: b, AgentFiles: []string{}})
	if err == nil {
		t.Fatal("expected error for empty agent files")
	}
	if !strings.Contains(err.Error(), "at least one agent file path is required") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestMiddleware_GenerateError(t *testing.T) {
	b := newMemBackend()
	// Do NOT set the file, so backend.Read will fail.

	ctx := context.Background()
	mw, err := New(ctx, &Config{Backend: b, AgentFiles: []string{"/missing.md"}})
	if err != nil {
		t.Fatal(err)
	}

	mock := &mockModel{}
	wrapped, err := mw.WrapModel(ctx, mock, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = wrapped.Generate(ctx, []*schema.Message{{Role: schema.User, Content: "hi"}})
	if err == nil {
		t.Fatal("expected error when backend read fails")
	}
	if !strings.Contains(err.Error(), "failed to load agent files") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMiddleware_StreamError(t *testing.T) {
	b := newMemBackend()
	// Do NOT set the file, so backend.Read will fail.

	ctx := context.Background()
	mw, err := New(ctx, &Config{Backend: b, AgentFiles: []string{"/missing.md"}})
	if err != nil {
		t.Fatal(err)
	}

	mock := &mockModel{}
	wrapped, err := mw.WrapModel(ctx, mock, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = wrapped.Stream(ctx, []*schema.Message{{Role: schema.User, Content: "hi"}})
	if err == nil {
		t.Fatal("expected error when backend read fails for stream")
	}
	if !strings.Contains(err.Error(), "failed to load agent files") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoader_DuplicateTopLevelFiles(t *testing.T) {
	// Same file listed twice in AgentFiles; second should be deduped via seen map.
	b := newMemBackend()
	b.set("/agent.md", "unique content")

	l := newLoader(b, []string{"/agent.md", "/agent.md"}, 0)
	content, err := l.load(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	count := strings.Count(content, "Contents of /agent.md")
	if count != 1 {
		t.Fatalf("expected /agent.md section once (deduped), got %d", count)
	}
}

func TestLoader_LoadFileError(t *testing.T) {
	// Top-level file that fails to read should propagate error from load().
	b := newMemBackend()
	// /missing.md is not set

	l := newLoader(b, []string{"/missing.md"}, 0)
	_, err := l.load(context.Background())
	if err == nil {
		t.Fatal("expected error for missing top-level file")
	}
	if !strings.Contains(err.Error(), "failed to load") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoader_MaxBytesStopsImports(t *testing.T) {
	// When budget is exhausted, further imports in collectImports should be skipped.
	b := newMemBackend()
	b.set("/main.md", "@/big.md @/small.md")
	b.set("/big.md", strings.Repeat("B", 200))
	b.set("/small.md", "SMALL")

	l := newLoader(b, []string{"/main.md"}, 50)
	content, err := l.load(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// main.md itself is loaded (always), big.md pushes over budget,
	// small.md should be skipped.
	if !strings.Contains(content, "Contents of /main.md") {
		t.Fatal("main.md should be present")
	}
	if strings.Contains(content, "SMALL") {
		t.Fatal("small.md should be skipped after budget exhausted")
	}
}

func TestLoader_ExactOutput(t *testing.T) {
	// Verify the exact output format matches the expected structure:
	// each file (top-level and imported) gets its own "Contents of ..." section,
	// @path references are preserved in the original text.
	b := newMemBackend()
	b.set("/project/CLAUDE.md", "this is project claude.md\n\n- git workflow @git/git-instructions.md")
	b.set("/project/git/git-instructions.md", "this is git-instructions.md")

	l := newLoader(b, []string{"/project/CLAUDE.md"}, 0)
	content, err := l.load(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	expected := `<system-reminder>
As you answer the user's questions, you can use the following context:
Codebase and user instructions are shown below. Be sure to adhere to these instructions. IMPORTANT: These instructions OVERRIDE any default behavior and you MUST follow them exactly as written.

Contents of /project/CLAUDE.md (instructions):

this is project claude.md

- git workflow @git/git-instructions.md

Contents of /project/git/git-instructions.md (instructions):

this is git-instructions.md
IMPORTANT: this context may or may not be relevant to your tasks. You should not respond to this context unless it is highly relevant to your task.
</system-reminder>`

	if content != expected {
		t.Fatalf("output mismatch.\n\ngot:\n%s\n\nexpected:\n%s", content, expected)
	}
}
