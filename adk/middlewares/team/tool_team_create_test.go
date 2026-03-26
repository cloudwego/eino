package team

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/adk"
)

func newTestTeamMiddleware() (*teamMiddleware, *Config) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}
	conf.ensureInit()

	runnerConf := &RunnerConfig{
		TeamConfig:  conf,
		AgentConfig: &adk.ChatModelAgentConfig{Name: "test", Description: "test"},
	}

	router := newSourceRouter(LeaderAgentName, nopLogger{})
	pumpMgr := newPumpManager(router, nopLogger{})
	mw := newTeamLeadMiddleware(runnerConf, router, pumpMgr)
	return mw, conf
}

func TestTeamCreateTool_Info(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool := newTeamCreateTool(mw)

	info, err := tool.Info(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, teamCreateToolName, info.Name)
}

func TestTeamCreateTool_InvokableRun_EmptyTeamName(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool := newTeamCreateTool(mw)

	_, err := tool.InvokableRun(context.Background(), `{"team_name":""}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "team_name is required")
}

func TestTeamCreateTool_InvokableRun_InvalidTeamName(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool := newTeamCreateTool(mw)

	_, err := tool.InvokableRun(context.Background(), `{"team_name":"bad/name"}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid characters")
}

func TestTeamCreateTool_InvokableRun_Success(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool := newTeamCreateTool(mw)

	result, err := tool.InvokableRun(context.Background(), `{"team_name":"myteam"}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "team_name")
	assert.Contains(t, result, "team_file_path")
	assert.Contains(t, result, "lead_agent_id")
	assert.Equal(t, "myteam", mw.getTeamName())
}

func TestTeamCreateTool_InvokableRun_TeamAlreadyActive(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool := newTeamCreateTool(mw)

	_, err := tool.InvokableRun(context.Background(), `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	_, err = tool.InvokableRun(context.Background(), `{"team_name":"another"}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already active")
}

func TestTeamCreateTool_InvokableRun_InvalidJSON(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool := newTeamCreateTool(mw)

	_, err := tool.InvokableRun(context.Background(), `not json`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse TeamCreate args")
}

func TestNewTeamCreateTool_NonNil(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool := newTeamCreateTool(mw)
	assert.NotNil(t, tool)
}

func TestMakeShutdownApprovalHandler(t *testing.T) {
	mw, conf := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	teamName := mw.getTeamName()

	cm := conf.configStore()
	err = cm.AddMember(ctx, teamName, teamMember{Name: "worker", JoinedAt: time.Now()})
	assert.NoError(t, err)

	inboxPath := inboxFilePath(conf.BaseDir, teamName, "worker")
	err = conf.Backend.Write(ctx, &WriteRequest{FilePath: inboxPath, Content: "[]"})
	assert.NoError(t, err)

	_, cancel := context.WithCancel(context.Background())
	mw.lifecycle.registry.register("worker", &teammateHandle{Cancel: cancel})

	handler := createTool.makeShutdownApprovalHandler(teamName)
	msg, err := handler(ctx, "worker")
	assert.NoError(t, err)
	assert.Contains(t, msg, "worker")
	assert.Contains(t, msg, "shut down")
}
