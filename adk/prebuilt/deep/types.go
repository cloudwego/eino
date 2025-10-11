package deep

const (
	generalAgentName = "general-purpose"
	taskToolName     = "task"
)

const (
	SessionKeyTodos = "deep_agent_session_key_todos"
)

func convSliceType[T, S any](slice []T, conv func(t T) S) []S {
	ret := make([]S, 0, len(slice))
	for _, t := range slice {
		ret = append(ret, conv(t))
	}
	return ret
}
