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

package team

import (
	"fmt"
	"time"

	"github.com/bytedance/sonic"
)

func parseMessageType(typeStr string) (messageType, error) {
	mt := messageType(typeStr)
	if _, ok := sendMessageTypeRules[mt]; ok {
		return mt, nil
	}
	return "", fmt.Errorf("unsupported message type %q", typeStr)
}

type shutdownRequestPayload struct {
	Type      string `json:"type"`
	RequestID string `json:"requestId,omitempty"`
	From      string `json:"from,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
}

type shutdownApprovalPayload struct {
	Type      string `json:"type"`
	RequestID string `json:"requestId,omitempty"`
	Approve   bool   `json:"approve"`
	From      string `json:"from,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
}

type planApprovalResponsePayload struct {
	Type      string `json:"type"`
	RequestID string `json:"requestId,omitempty"`
	Approve   bool   `json:"approve"`
	Feedback  string `json:"feedback,omitempty"`
	From      string `json:"from,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
}

func marshalShutdownRequest(fromName, requestID, reason string) (string, error) {
	return sonic.MarshalString(shutdownRequestPayload{
		Type:      string(messageTypeShutdownRequest),
		RequestID: requestID,
		From:      fromName,
		Reason:    reason,
		Timestamp: utcNowMillis(),
	})
}

func marshalShutdownApproval(fromName, requestID string, approve bool, reason string) (string, error) {
	return sonic.MarshalString(shutdownApprovalPayload{
		Type:      string(messageTypeShutdownApproved),
		RequestID: requestID,
		Approve:   approve,
		From:      fromName,
		Reason:    reason,
		Timestamp: utcNowMillis(),
	})
}

func marshalPlanApprovalResponse(fromName, requestID string, approve bool, feedback string) (string, error) {
	return sonic.MarshalString(planApprovalResponsePayload{
		Type:      string(messageTypePlanApprovalResponse),
		RequestID: requestID,
		Approve:   approve,
		Feedback:  feedback,
		From:      fromName,
		Timestamp: utcNowMillis(),
	})
}

func decodeShutdownRequest(text string) (shutdownRequestPayload, error) {
	var raw struct {
		Type        string `json:"type"`
		RequestID   string `json:"requestId,omitempty"`
		RequestIDV1 string `json:"request_id,omitempty"`
		From        string `json:"from,omitempty"`
		Reason      string `json:"reason,omitempty"`
		Timestamp   string `json:"timestamp,omitempty"`
	}
	if err := sonic.UnmarshalString(text, &raw); err != nil {
		return shutdownRequestPayload{}, err
	}
	return shutdownRequestPayload{
		Type:      raw.Type,
		RequestID: resolveLegacyRequestID(raw.RequestID, raw.RequestIDV1),
		From:      raw.From,
		Reason:    raw.Reason,
		Timestamp: raw.Timestamp,
	}, nil
}

func decodeShutdownApproval(text string) (shutdownApprovalPayload, error) {
	var raw struct {
		Type        string `json:"type"`
		RequestID   string `json:"requestId,omitempty"`
		RequestIDV1 string `json:"request_id,omitempty"`
		Approve     bool   `json:"approve"`
		From        string `json:"from,omitempty"`
		Reason      string `json:"reason,omitempty"`
		Timestamp   string `json:"timestamp,omitempty"`
	}
	if err := sonic.UnmarshalString(text, &raw); err != nil {
		return shutdownApprovalPayload{}, err
	}
	return shutdownApprovalPayload{
		Type:      raw.Type,
		RequestID: resolveLegacyRequestID(raw.RequestID, raw.RequestIDV1),
		Approve:   raw.Approve,
		From:      raw.From,
		Reason:    raw.Reason,
		Timestamp: raw.Timestamp,
	}, nil
}

func resolveLegacyRequestID(requestID, legacyRequestID string) string {
	if requestID != "" {
		return requestID
	}
	return legacyRequestID
}

func utcNowMillis() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}
