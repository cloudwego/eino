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

package adk

import (
	"bytes"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

// TestDecodeSessionEventHex decodes a Gob-encoded SessionEvent from a hex
// string and prints the result as JSON. Paste the hex bytes into the
// gobHex constant below and run:
//
//	go test ./adk -run TestDecodeSessionEventHex -v
//
// The hex string may contain whitespace (spaces, newlines, tabs) which is
// stripped automatically.
func TestDecodeSessionEventHex(t *testing.T) {
	const gobHex = ``

	clean := stripWhitespace(gobHex)
	if clean == "" {
		t.Skip("paste hex bytes into gobHex to decode")
	}

	raw, err := hex.DecodeString(clean)
	if err != nil {
		t.Fatalf("hex decode: %v", err)
	}

	var event SessionEvent[*schema.AgenticMessage]
	if err := gob.NewDecoder(bytes.NewReader(raw)).Decode(&event); err != nil {
		t.Fatalf("gob decode: %v", err)
	}

	out, err := json.MarshalIndent(&event, "", "  ")
	if err != nil {
		t.Fatalf("json marshal: %v", err)
	}
	t.Logf("decoded SessionEvent:\n%s", out)
}

func stripWhitespace(s string) string {
	r := strings.NewReplacer(" ", "", "\n", "", "\r", "", "\t", "")
	return r.Replace(s)
}
