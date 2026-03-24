package team

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type inMemoryBackend struct {
	files map[string]string
	dirs  map[string]struct{}
	mu    sync.RWMutex
}

func newInMemoryBackend() *inMemoryBackend {
	return &inMemoryBackend{
		files: make(map[string]string),
		dirs:  make(map[string]struct{}),
	}
}

func (b *inMemoryBackend) LsInfo(_ context.Context, req *LsInfoRequest) ([]FileInfo, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	reqPath := filepath.Clean(req.Path)
	var result []FileInfo
	for path := range b.files {
		if filepath.Dir(path) == reqPath {
			result = append(result, FileInfo{Path: path})
		}
	}
	return result, nil
}

func (b *inMemoryBackend) Read(_ context.Context, req *ReadRequest) (string, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	content, ok := b.files[req.FilePath]
	if !ok {
		return "", errTeamNotFound
	}
	return content, nil
}

func (b *inMemoryBackend) Write(_ context.Context, req *WriteRequest) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.files[req.FilePath] = req.Content
	b.dirs[filepath.Dir(req.FilePath)] = struct{}{}
	return nil
}

func (b *inMemoryBackend) Delete(_ context.Context, req *DeleteRequest) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	target := filepath.Clean(req.FilePath)
	prefix := target + string(filepath.Separator)
	for path := range b.files {
		if path == target || len(path) > len(prefix) && path[:len(prefix)] == prefix {
			delete(b.files, path)
		}
	}
	for dir := range b.dirs {
		if dir == target || len(dir) > len(prefix) && dir[:len(prefix)] == prefix {
			delete(b.dirs, dir)
		}
	}
	return nil
}

func (b *inMemoryBackend) Exists(_ context.Context, path string) (bool, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	clean := filepath.Clean(path)
	if _, ok := b.files[clean]; ok {
		return true, nil
	}
	_, ok := b.dirs[clean]
	return ok, nil
}

func (b *inMemoryBackend) Mkdir(_ context.Context, path string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.dirs[filepath.Clean(path)] = struct{}{}
	return nil
}

func TestRemoveTeammateIgnoresTaskParseErrors(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/team"

	conf := &Config{
		Backend:    backend,
		BaseDir:    baseDir,
		InboxLocks: newInboxLockManager(),
		TaskLock:   &sync.Mutex{},
	}

	store := newTeamConfigStoreFromConfig(conf)
	teamCfg, err := store.CreateTeam(ctx, "alpha", "desc", LeaderAgentName, "")
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	worker := teamMember{
		Name:      "worker",
		AgentID:   makeAgentID("worker", teamCfg.Name),
		JoinedAt:  time.Now(),
		AgentType: generalAgentName,
	}
	if err := store.AddMember(ctx, teamCfg.Name, worker); err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	taskPath := filepath.Join(tasksDirPath(baseDir, teamCfg.Name), "1.json")
	if err := backend.Write(ctx, &WriteRequest{FilePath: taskPath, Content: "{"}); err != nil {
		t.Fatalf("write malformed task: %v", err)
	}

	mw := &teamMiddleware{
		conf:      &RunnerConfig{TeamConfig: conf},
		teammates: &sync.Map{},
	}

	unassigned, err := mw.removeTeammate(ctx, teamCfg.Name, worker.Name)
	if err != nil {
		t.Fatalf("removeTeammate: %v", err)
	}
	if len(unassigned) != 0 {
		t.Fatalf("expected no unassigned tasks, got %v", unassigned)
	}

	exists, err := store.HasMember(ctx, teamCfg.Name, worker.Name)
	if err != nil {
		t.Fatalf("HasMember: %v", err)
	}
	if exists {
		t.Fatalf("expected worker to be removed from team config")
	}
}

func TestNewFileMailboxDefaultsMaxInboxMessages(t *testing.T) {
	mb := newFileMailbox(&mailboxConfig{
		Backend:   newInMemoryBackend(),
		BaseDir:   "/tmp/team",
		TeamName:  "alpha",
		OwnerName: LeaderAgentName,
	})

	if mb.conf.MaxInboxMessages != 1000 {
		t.Fatalf("expected default MaxInboxMessages=1000, got %d", mb.conf.MaxInboxMessages)
	}
}

func TestFormatTeammateMessageEnvelopePreservesTextPayload(t *testing.T) {
	text := `{"note":"5 < 7 and \"quoted\""}`
	got := formatTeammateMessageEnvelope("worker", text, "summary")

	if !strings.Contains(got, text) {
		t.Fatalf("expected original payload to remain readable in envelope, got %q", got)
	}
	if strings.Contains(got, "&quot;") || strings.Contains(got, "&lt; 7") {
		t.Fatalf("expected payload text not to be XML-escaped, got %q", got)
	}
}
