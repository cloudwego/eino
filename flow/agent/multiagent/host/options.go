/*
 * Copyright 2024 CloudWeGo Authors
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

package host

import "github.com/cloudwego/eino/flow/agent"

type options struct {
	agentCallbacks []MultiAgentCallback
}

// WithAgentCallbacks registers callbacks to be invoked by the host multi-agent.
func WithAgentCallbacks(agentCallbacks ...MultiAgentCallback) agent.AgentOption {
	return agent.WrapImplSpecificOptFn(func(opts *options) {
		opts.agentCallbacks = append(opts.agentCallbacks, agentCallbacks...)
	})
}
