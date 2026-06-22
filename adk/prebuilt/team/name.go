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

// name.go provides a single source of truth for validating team and member
// names before they are used to build filesystem paths (team dir, inbox file,
// task dir) or routed through the mailbox. Names flow in from LLM tool calls
// (TeamCreate.team_name, Agent.name) and TaskUpdate.owner, so they must be
// constrained to avoid path traversal, reserved-name collisions, and the
// broadcast wildcard before any Join/Write happens.

package team

import (
	"fmt"
	"strings"
)

// maxNameLength bounds names so a single component cannot blow past common
// filesystem limits once combined with directory prefixes and the ".json" suffix.
const maxNameLength = 128

// isNameStartChar reports whether r is a valid first character for a name:
// only ASCII letters or digits, so a name can never begin with ".", "-", or a
// separator that could be interpreted specially by the filesystem.
func isNameStartChar(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// isNameBodyChar reports whether r is allowed in the body of a name: letters,
// digits, and the safe punctuation ".", "_", "-". Notably excludes "*" (the
// broadcast wildcard), "/" and "\\" (path separators), and whitespace.
func isNameBodyChar(r rune) bool {
	return isNameStartChar(r) || r == '.' || r == '_' || r == '-'
}

// validateName checks that name is safe to embed in a filesystem path and to use
// as a mailbox address. kind is used only for error messages (e.g. "team name",
// "member name"). The rules are intentionally strict:
//
//   - non-empty and at most maxNameLength characters
//   - must start with an ASCII letter or digit
//   - may otherwise contain only letters, digits, '.', '_', '-'
//   - the special path segments "." and ".." are rejected outright
//   - the broadcast wildcard "*" can never appear (covered by the charset, but
//     guarded explicitly for a clearer error)
func validateName(kind, name string) error {
	if name == "" {
		return fmt.Errorf("%s is required", kind)
	}
	if len(name) > maxNameLength {
		return fmt.Errorf("%s %q is too long (max %d characters)", kind, name, maxNameLength)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("%s %q is reserved and cannot be used", kind, name)
	}
	if strings.Contains(name, broadcastTarget) {
		return fmt.Errorf("%s %q must not contain %q (reserved for broadcast)", kind, name, broadcastTarget)
	}
	for i, r := range name {
		if i == 0 {
			if !isNameStartChar(r) {
				return fmt.Errorf("%s %q must start with a letter or digit", kind, name)
			}
			continue
		}
		if !isNameBodyChar(r) {
			return fmt.Errorf("%s %q contains invalid character %q (allowed: letters, digits, '.', '_', '-')", kind, name, r)
		}
	}
	return nil
}

// validateTeamName validates a team name supplied via the TeamCreate tool.
func validateTeamName(name string) error {
	return validateName("team name", name)
}

// validateMemberName validates a teammate name supplied via the Agent tool or a
// TaskUpdate owner. In addition to the shared character rules, the reserved
// leader name "team-lead" is rejected so a regular teammate can never shadow the
// leader's inbox or routing identity.
func validateMemberName(name string) error {
	if err := validateName("member name", name); err != nil {
		return err
	}
	if name == LeaderAgentName {
		return fmt.Errorf("member name %q is reserved for the team leader", name)
	}
	return nil
}

// suffixedMemberName builds the deduplicated name "<base>-<i>" used when a
// teammate name collides with an existing member. The "-<i>" suffix can push a
// near-maxNameLength base past maxNameLength, so the base is truncated to leave
// room for the suffix. Trailing body-only characters (".", "_", "-") are trimmed
// from the truncated base so the suffix never produces sequences like "name.-2".
// The base always retains its first (validated) start character, so the result
// is never empty and still satisfies validateMemberName.
func suffixedMemberName(base string, i int) string {
	return appendSuffixWithinLimit(base, fmt.Sprintf("-%d", i))
}

// appendSuffixWithinLimit appends suffix to base while keeping the combined
// length within maxNameLength. When base is too long it is truncated and its
// trailing body-only characters (".", "_", "-") are trimmed so the suffix never
// produces sequences like "name.-2". base must already start with a valid start
// character, which is preserved, so the result is never empty.
func appendSuffixWithinLimit(base, suffix string) string {
	budget := maxNameLength - len(suffix)
	if budget < 1 {
		budget = 1
	}
	if len(base) > budget {
		base = strings.TrimRight(base[:budget], "._-")
	}
	return base + suffix
}
