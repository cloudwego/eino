/*
 * Copyright 2025 CloudWeGo Authors
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

// Package skill provides a Skill middleware, types, and a local filesystem backend.
//
// # Overview
//
// The Skill middleware is a ChatModelAgentMiddleware implementation that injects:
//   - a system instruction (Skills System)
//   - a tool (default name: "skill") to load and execute skills
//
// Skill definitions are stored in SKILL.md files with a YAML frontmatter. The frontmatter is
// parsed into FrontMatter (name/description/context/agent/model), and the remaining markdown
// is treated as the skill content.
//
// # Execution modes
//
// Skill execution is controlled by frontmatter "context":
//   - inline (default): returns the skill content as the tool result in the current agent
//   - fork: runs the skill with a new sub-agent without parent message history
//   - fork_with_context: runs the skill with a new sub-agent carrying parent message history
//
// # Extension points
//
//   - CustomToolParams customizes the tool parameter schema.
//   - BuildContent customizes how skill content is generated from raw tool arguments.
//   - BuildForkMessages customizes the initial messages passed to the sub-agent in fork modes.
//   - FormatForkResult customizes how sub-agent outputs are formatted back to the caller.
//
// # Filesystem backend
//
// NewBackendFromFilesystem loads skills from a filesystem backend. It scans only first-level
// subdirectories under BaseDir and reads each <dir>/SKILL.md as a skill definition.
package skill
