package automemory

import (
	"github.com/eino-contrib/jsonschema"
)

// GetTopicMemorySelectionOutputFormat output format which model MUST follow.
func GetTopicMemorySelectionOutputFormat() jsonschema.Schema {
	return *topicMemorySelectionJSONSchema
}

var topicMemorySelectionJSONSchema *jsonschema.Schema

func init() {
	topicMemorySelectionJSONSchema = &jsonschema.Schema{
		Type:       "object",
		Properties: jsonschema.NewProperties(),
		Required:   []string{"selected_memories"},
	}

	topicMemorySelectionJSONSchema.Properties.Set("selected_memories", &jsonschema.Schema{
		Type: "array",
		Items: &jsonschema.Schema{
			Type: "string",
		},
	})
}

const (
	memorySearchPattern = "*.md"
	memoryIndexFileName = "MEMORY.md"
)
