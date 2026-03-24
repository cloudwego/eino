package team

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestTeamDeleteTool_Info(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool := newTeamDeleteTool(mw)

	info, err := tool.Info(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, teamDeleteToolName, info.Name)
}

func TestTeamDeleteTool_InvokableRun_NoActiveTeam(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool := newTeamDeleteTool(mw)

	_, err := tool.InvokableRun(context.Background(), "")
	assert.ErrorIs(t, err, errTeamNotFound)
}

func TestTeamDeleteTool_InvokableRun_ActiveTeammates(t *testing.T) {
	mw, conf := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	cm := conf.configStore()
	err = cm.AddMember(ctx, mw.getTeamName(), teamMember{Name: "worker", JoinedAt: time.Now()})
	assert.NoError(t, err)

	deleteTool := newTeamDeleteTool(mw)
	result, err := deleteTool.InvokableRun(ctx, "")
	assert.NoError(t, err)
	assert.Contains(t, result, "active teammates")
	assert.Contains(t, result, `"success":false`)
}

func TestTeamDeleteTool_InvokableRun_Success(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)
	assert.Equal(t, "myteam", mw.getTeamName())

	deleteTool := newTeamDeleteTool(mw)
	result, err := deleteTool.InvokableRun(ctx, "")
	assert.NoError(t, err)
	assert.Contains(t, result, "success")
	assert.Equal(t, "", mw.getTeamName())
}
