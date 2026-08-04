package acp

import (
	"encoding/json"
	"log"
	"strings"
)

// reservedMetaKeys is the set of keys that MindFS reserves in tool-call meta.
// Agent-supplied values for these keys are silently dropped by mergeToolCallMeta
// to prevent agents from forging MindFS UI state (e.g. pre-answering elicitation
// cards, hijacking shell-stream merging, or inflating session history).
// Keys are logged (without values) so we can detect agents that legitimately
// need a key and act accordingly.
var reservedMetaKeys = map[string]bool{
	"questions":        true,
	"answers":          true,
	"title":            true,
	"source":           true,
	"phase":            true,
	"shell":            true,
	"output":           true,
	"input":            true,
	"filePath":         true,
	"path":             true,
	"replayTruncated":  true,
	"replayTruncation": true,
	"source_session":   true,
	"toolUseId":        true,
	"parentToolUseId":  true,
	"cancelled":        true,
	"exitCode":         true,
	"error":            true,
	"taskTool":         true,
}

// maxAgentMetaBytes is the maximum JSON-serialized size of the sanitized agent
// meta (excluding TrustedMeta). ask_user and edit tool calls walk the
// PreserveToolCallContent path and are written verbatim to session JSONL, so
// a large blob would bloat history files and be rebroadcast on replay.
const maxAgentMetaBytes = 8 * 1024

// mergeToolCallMeta combines trusted MindFS meta with sanitized agent meta.
//
//  1. Agent meta keys that are in reservedMetaKeys are stripped (and logged
//     by agent name and tool-call id so we can identify false positives).
//  2. The sanitized agent meta is size-checked: if its JSON encoding exceeds
//     maxAgentMetaBytes the whole agent contribution is dropped.
//  3. TrustedMeta keys are overlaid last, so they always win.
//  4. Returns nil when both inputs are empty to avoid changing nil-ness checks
//     in downstream code.
func mergeToolCallMeta(agentName, toolCallID string, trustedMeta map[string]any, agentMeta map[string]any) map[string]any {
	// Filter reserved keys from agent meta.
	var sanitized map[string]any
	var dropped []string
	for k, v := range agentMeta {
		if reservedMetaKeys[k] {
			dropped = append(dropped, k)
			continue
		}
		if sanitized == nil {
			sanitized = make(map[string]any)
		}
		sanitized[k] = v
	}
	if len(dropped) > 0 {
		log.Printf("[agent/acp] meta.sanitize agent=%s tool_call_id=%s dropped_keys=%s",
			agentName, toolCallID, strings.Join(dropped, ","))
	}

	// Enforce size limit on the sanitized agent contribution.
	if len(sanitized) > 0 {
		raw, err := json.Marshal(sanitized)
		if err != nil || len(raw) > maxAgentMetaBytes {
			log.Printf("[agent/acp] meta.oversize agent=%s tool_call_id=%s bytes=%d limit=%d dropped=all",
				agentName, toolCallID, len(raw), maxAgentMetaBytes)
			sanitized = nil
		}
	}

	if len(sanitized) == 0 && len(trustedMeta) == 0 {
		return nil
	}

	out := make(map[string]any, len(sanitized)+len(trustedMeta))
	for k, v := range sanitized {
		out[k] = v
	}
	// TrustedMeta always wins over agent meta.
	for k, v := range trustedMeta {
		out[k] = v
	}
	return out
}
