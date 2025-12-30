package skill

import (
	"context"
)

type inMemoryBackend struct {
	m map[string]Skill
}

func (i *inMemoryBackend) List(ctx context.Context) ([]Skill, error) {
	var skills []Skill
	for _, skill := range i.m {
		skills = append(skills, skill)
	}
	return skills, nil
}

func (i *inMemoryBackend) Get(ctx context.Context, name string) (Skill, error) {
	
}
