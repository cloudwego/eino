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

package summarization

import (
	"context"
	"fmt"
	"strings"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

type PreserveSkillsConfig struct {
	// MaxSkills limits the maximum number of skills to preserve.
	// = 0 means do not preserve any skills (disabled).
	// > 0 means preserve up to this many most recent skills.
	// Optional. Defaults to 5.
	MaxSkills *int

	// SkillToolName is the specific name of the tool used for loading skills.
	// Only tool calls matching this name will be considered as skills for preservation.
	// Optional. Defaults to "skill".
	SkillToolName string

	// Next is the next FinalizeFunc to call after preserving skills.
	// Optional.
	Next FinalizeFunc
}

func (c *PreserveSkillsConfig) check() error {
	if c.MaxSkills != nil && *c.MaxSkills < 0 {
		return fmt.Errorf("MaxSkills must be non-negative")
	}
	return nil
}

// PreserveSkillsFinalizer returns a FinalizeFunc that preserves skills loaded by the ADK skill middleware in the summary message.
// It extracts skill contents from the corresponding tool calls and prepends them to the summary message,
// ensuring the agent retains these skills knowledge after the context window is compacted.
func PreserveSkillsFinalizer(_ context.Context, config PreserveSkillsConfig) (FinalizeFunc, error) {
	if err := config.check(); err != nil {
		return nil, err
	}

	return func(ctx context.Context, originalMessages []adk.Message, summary adk.Message) ([]adk.Message, error) {
		summarizeInputMsgs, _ := ctx.Value(ctxKeySummarizeInput).([]adk.Message)
		if len(summarizeInputMsgs) == 0 {
			panic("impossible: summarizeInputMsgs is empty")
		}

		skillText, err := buildPreservedSkillsText(ctx, summarizeInputMsgs, &config)
		if err != nil {
			return nil, err
		}

		if skillText != "" {
			parts := []schema.MessageInputPart{
				{
					Type: schema.ChatMessagePartTypeText,
					Text: skillText,
				},
			}

			if summary.UserInputMultiContent != nil {
				parts = append(parts, summary.UserInputMultiContent...)
			} else if summary.Content != "" {
				parts = append(parts, schema.MessageInputPart{
					Type: schema.ChatMessagePartTypeText,
					Text: summary.Content,
				})
			}

			summary.UserInputMultiContent = parts
			summary.Content = ""
		}

		if config.Next != nil {
			return config.Next(ctx, originalMessages, summary)
		}

		return []adk.Message{summary}, nil
	}, nil
}

type skillInfo struct {
	Name    string
	Content string
}

func buildPreservedSkillsText(_ context.Context, messages []adk.Message, config *PreserveSkillsConfig) (string, error) {
	const defaultSkillTool = "skill"

	if config == nil {
		config = &PreserveSkillsConfig{}
	}

	maxSkills := 5
	if config.MaxSkills != nil {
		maxSkills = *config.MaxSkills
	}
	if maxSkills <= 0 {
		return "", nil
	}

	skillTool := defaultSkillTool
	if config.SkillToolName != "" {
		skillTool = config.SkillToolName
	}

	var skills []*skillInfo
	argsMap := make(map[string]string)

	for _, msg := range messages {
		switch msg.Role {
		case schema.Assistant:
			for _, tc := range msg.ToolCalls {
				if tc.Function.Name == skillTool {
					argsMap[tc.ID] = tc.Function.Arguments
				}
			}
		case schema.Tool:
			arguments, ok := argsMap[msg.ToolCallID]
			if !ok {
				continue
			}

			var arg struct {
				Skill string `json:"skill"`
			}
			if err := sonic.UnmarshalString(arguments, &arg); err != nil {
				return "", fmt.Errorf("failed to parse skill arguments from tool call %s: %w", msg.ToolCallID, err)
			}

			skills = append(skills, &skillInfo{
				Name:    arg.Skill,
				Content: msg.Content,
			})
		}
	}

	if len(skills) == 0 {
		return "", nil
	}

	// Remove duplicate skills by name, keeping the latest one
	uniqueSkills := make([]*skillInfo, 0, len(skills))
	seenNames := make(map[string]bool)
	for i := len(skills) - 1; i >= 0; i-- {
		skill := skills[i]
		if !seenNames[skill.Name] {
			seenNames[skill.Name] = true
			uniqueSkills = append(uniqueSkills, skill)
		}
	}

	// Reverse back to chronological order
	for i, j := 0, len(uniqueSkills)-1; i < j; i, j = i+1, j-1 {
		uniqueSkills[i], uniqueSkills[j] = uniqueSkills[j], uniqueSkills[i]
	}
	skills = uniqueSkills

	if len(skills) > maxSkills {
		skills = skills[len(skills)-maxSkills:]
	}

	var parts []string
	for _, skill := range skills {
		parts = append(parts, fmt.Sprintf(skillSectionFormat, skill.Name, skill.Content))
	}

	skillsText := strings.Join(parts, "\n\n---\n\n")
	skillsText = fmt.Sprintf(getSkillPreamble(), skillsText)
	skillsText = fmt.Sprintf("<system-reminder>\n%s"+"\n</system-reminder>", skillsText)

	return skillsText, nil
}
