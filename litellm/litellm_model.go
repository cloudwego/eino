package litellm

import (
	"github.com/cloudwego/eino-ext/libs/acl/openai"
)

// LitellmModel represents a model that interacts with OpenAI-compatible APIs.
type LitellmModel struct {
	client *openai.Client
}

// NewLitellmModel creates a new instance of LitellmModel.
func NewLitellmModel(apiKey string) (*LitellmModel, error) {
	client, err := openai.NewClient(apiKey)
	if err != nil {
		return nil, err
	}
	return &LitellmModel{client: client}, nil
}

// GenerateText generates text based on the given prompt.
func (m *LitellmModel) GenerateText(prompt string) (string, error) {
	response, err := m.client.Completions.Create(prompt)
	if err != nil {
		return "", err
	}
	return response.Choices[0].Text, nil
}

// Litellm is a widely used tool in the AI community that allows deploying a proxy and providing access via HTTP API. Additionally, the Litellm API is compatible with the OpenAI API protocol.
