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

package compose

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

type stubSerializer struct {
	unmarshal func(data []byte, v any) error
	marshal   func(v any) ([]byte, error)
}

func (s stubSerializer) Marshal(v any) ([]byte, error) {
	return s.marshal(v)
}

func (s stubSerializer) Unmarshal(data []byte, v any) error {
	return s.unmarshal(data, v)
}

func TestMigrateCheckpointState_UnmarshalError(t *testing.T) {
	in := []byte("in")
	ser := stubSerializer{
		unmarshal: func(_ []byte, _ any) error { return errors.New("bad") },
		marshal:   func(_ any) ([]byte, error) { return []byte("unused"), nil },
	}
	_, err := MigrateCheckpointState(in, ser, func(state any) (any, bool, error) {
		return state, false, nil
	})
	assert.Error(t, err)
}

func TestMigrateCheckpointState_NoChangeReturnsOriginalBytes(t *testing.T) {
	in := []byte("in")
	cp := &checkpoint{State: "s"}
	ser := stubSerializer{
		unmarshal: func(_ []byte, v any) error {
			*(v.(*checkpoint)) = *cp
			return nil
		},
		marshal: func(_ any) ([]byte, error) {
			return []byte("marshaled"), nil
		},
	}
	out, err := MigrateCheckpointState(in, ser, func(state any) (any, bool, error) {
		return state, false, nil
	})
	assert.NoError(t, err)
	assert.Equal(t, in, out)
}

func TestMigrateCheckpointState_ChangeTriggersMarshal(t *testing.T) {
	in := []byte("in")
	cp := &checkpoint{State: "s"}
	var sawState any
	ser := stubSerializer{
		unmarshal: func(_ []byte, v any) error {
			*(v.(*checkpoint)) = *cp
			return nil
		},
		marshal: func(v any) ([]byte, error) {
			sawState = v.(*checkpoint).State
			return []byte("marshaled"), nil
		},
	}
	out, err := MigrateCheckpointState(in, ser, func(state any) (any, bool, error) {
		return "s2", true, nil
	})
	assert.NoError(t, err)
	assert.Equal(t, []byte("marshaled"), out)
	assert.Equal(t, "s2", sawState)
}

func TestMigrateCheckpointState_MigrateErrorStops(t *testing.T) {
	in := []byte("in")
	cp := &checkpoint{
		State: "root",
		SubGraphs: map[string]*checkpoint{
			"sub": {State: "sub"},
		},
	}
	ser := stubSerializer{
		unmarshal: func(_ []byte, v any) error {
			*(v.(*checkpoint)) = *cp
			return nil
		},
		marshal: func(_ any) ([]byte, error) {
			return []byte("marshaled"), nil
		},
	}
	_, err := MigrateCheckpointState(in, ser, func(state any) (any, bool, error) {
		if state == "sub" {
			return nil, false, errors.New("boom")
		}
		return state, false, nil
	})
	assert.Error(t, err)
}
