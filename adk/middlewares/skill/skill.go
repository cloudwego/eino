package skill

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"text/template"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type Skill struct {
	Name        string
	Description string

	Content string

	BaseDirectory string

	Extra map[string]any
}

type Backend interface {
	List(ctx context.Context) ([]Skill, error)
	Get(ctx context.Context, name string) (Skill, error)
}

type Config struct {
	Backend Backend
}

func New(ctx context.Context, config *Config) (adk.AgentMiddleware, error) {
	if config == nil {
		return adk.AgentMiddleware{}, fmt.Errorf("config is required")
	}
	if config.Backend == nil {
		return adk.AgentMiddleware{}, fmt.Errorf("backend is required")
	}
	return adk.AgentMiddleware{
		AdditionalTools: []tool.BaseTool{&skillTool{b: config.Backend}},
	}, nil
}

type skillTool struct {
	b Backend
}

type descriptionTemplateHelper struct {
	Skills []Skill
}

func (s *skillTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	skills, err := s.b.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list skills: %w", err)
	}

	desc, err := renderToolDescription(skills)
	if err != nil {
		return nil, fmt.Errorf("failed to render skill tool description: %w", err)
	}

	return &schema.ToolInfo{
		Name: "skill",
		Desc: skillToolDescriptionBase + desc,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"skill": {
				Type:     schema.String,
				Desc:     "The skill name (no arguments). E.g., \"pdf\" or \"xlsx\"",
				Required: true,
			},
		}),
	}, nil
}

type inputArguments struct {
	Skill string `json:"skill"`
}

func (s *skillTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	args := &inputArguments{}
	err := json.Unmarshal([]byte(argumentsInJSON), args)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal arguments: %w", err)
	}
	skill, err := s.b.Get(ctx, args.Skill)
	if err != nil {
		return "", fmt.Errorf("failed to get skill: %w", err)
	}

	return fmt.Sprintf(skillToolResult, skill.Name) + fmt.Sprintf(skillUserContent, skill.BaseDirectory, skill.Content), nil // claude-code 里，只有skillToolResult在 result 里，skillUserContent 会另外加到一个 user message 里
}

func renderToolDescription(skills []Skill) (string, error) {
	tpl, err := template.New("skills").Parse(skillToolDescriptionTemplate)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	err = tpl.Execute(&buf, descriptionTemplateHelper{Skills: skills})
	if err != nil {
		return "", err
	}

	return buf.String(), nil
}
