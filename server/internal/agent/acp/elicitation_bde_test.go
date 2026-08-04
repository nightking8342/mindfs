package acp

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"mindfs/server/internal/agent/types"

	acpsdk "github.com/coder/acp-go-sdk"
)

// ===== A: meta sanitization edge cases =====

func TestConvertEventDropsOversizedAgentMetaKeepsTrusted(t *testing.T) {
	update := SessionUpdate{
		Type:        UpdateTypeToolCall,
		AgentName:   "test-agent",
		SessionID:   "sess-1",
		TrustedMeta: map[string]any{"title": "trusted"},
		Raw: acpsdk.SessionUpdate{
			ToolCall: &acpsdk.SessionUpdateToolCall{
				ToolCallId: acpsdk.ToolCallId("tc-1"),
				Kind:       acpsdk.ToolKind("ask_user"),
				Meta:       map[string]any{"blob": strings.Repeat("x", maxAgentMetaBytes+1)},
			},
		},
	}
	ev := convertEvent(update)
	tc, ok := ev.Data.(types.ToolCall)
	if !ok {
		t.Fatalf("ev.Data = %#v, want types.ToolCall", ev.Data)
	}
	if tc.Meta["title"] != "trusted" {
		t.Fatalf("trusted meta lost: %#v", tc.Meta)
	}
	if _, ok := tc.Meta["blob"]; ok {
		t.Fatal("oversized agent meta must be dropped wholesale, not truncated")
	}
}

func TestConvertEventEmptyMetaIsNil(t *testing.T) {
	update := SessionUpdate{
		Type:      UpdateTypeToolCall,
		AgentName: "test-agent",
		SessionID: "sess-1",
		Raw: acpsdk.SessionUpdate{
			ToolCall: &acpsdk.SessionUpdateToolCall{
				ToolCallId: acpsdk.ToolCallId("tc-1"),
				Kind:       acpsdk.ToolKind("ask_user"),
			},
		},
	}
	ev := convertEvent(update)
	tc, ok := ev.Data.(types.ToolCall)
	if !ok {
		t.Fatalf("ev.Data = %#v, want types.ToolCall", ev.Data)
	}
	if tc.Meta != nil {
		t.Fatalf("Meta = %#v, want nil when both trusted and agent meta are empty", tc.Meta)
	}
}

// ===== B: oneOf / items.anyOf enum encodings =====

func TestParseElicitationFieldOneOf(t *testing.T) {
	field := parseElicitationField("strategy", map[string]any{
		"type": "string",
		"oneOf": []any{
			map[string]any{"const": "safe", "title": "Safe mode"},
			map[string]any{"const": "fast", "title": "Fast mode"},
		},
	})
	if want := []string{"Safe mode", "Fast mode"}; !reflect.DeepEqual(field.enum, want) {
		t.Fatalf("enum = %v, want %v", field.enum, want)
	}
	if want := []string{"safe", "fast"}; !reflect.DeepEqual(field.enumValues, want) {
		t.Fatalf("enumValues = %v, want %v", field.enumValues, want)
	}
}

func TestParseElicitationFieldOneOfFallsBackToConstAsLabel(t *testing.T) {
	field := parseElicitationField("strategy", map[string]any{
		"type":  "string",
		"oneOf": []any{map[string]any{"const": "safe"}},
	})
	if want := []string{"safe"}; !reflect.DeepEqual(field.enum, want) || !reflect.DeepEqual(field.enumValues, want) {
		t.Fatalf("enum=%v enumValues=%v, want both %v", field.enum, field.enumValues, want)
	}
}

func TestParseElicitationFieldEnumWinsOverOneOf(t *testing.T) {
	field := parseElicitationField("strategy", map[string]any{
		"type":  "string",
		"enum":  []any{"a", "b"},
		"oneOf": []any{map[string]any{"const": "x", "title": "X"}},
	})
	if want := []string{"a", "b"}; !reflect.DeepEqual(field.enum, want) || !reflect.DeepEqual(field.enumValues, want) {
		t.Fatalf("enum=%v enumValues=%v, want both %v", field.enum, field.enumValues, want)
	}
}

func TestParseElicitationFieldArrayAnyOf(t *testing.T) {
	field := parseElicitationField("tags", map[string]any{
		"type": "array",
		"items": map[string]any{
			"anyOf": []any{
				map[string]any{"const": "go", "title": "Go"},
				map[string]any{"const": "rust", "title": "Rust"},
			},
		},
	})
	if !field.multiSelect {
		t.Fatal("array field must be multi-select")
	}
	if want := []string{"Go", "Rust"}; !reflect.DeepEqual(field.enum, want) {
		t.Fatalf("enum = %v, want %v", field.enum, want)
	}
	if want := []string{"go", "rust"}; !reflect.DeepEqual(field.enumValues, want) {
		t.Fatalf("enumValues = %v, want %v", field.enumValues, want)
	}
}

func TestParseElicitationFieldOneOfSkipsConstlessEntries(t *testing.T) {
	field := parseElicitationField("strategy", map[string]any{
		"type": "string",
		"oneOf": []any{
			map[string]any{"title": "no const"},
			map[string]any{"const": "safe", "title": "Safe"},
		},
	})
	if want := []string{"Safe"}; !reflect.DeepEqual(field.enum, want) {
		t.Fatalf("enum = %v, want %v", field.enum, want)
	}
	if want := []string{"safe"}; !reflect.DeepEqual(field.enumValues, want) {
		t.Fatalf("enumValues = %v, want %v", field.enumValues, want)
	}
}

func TestParseElicitationFieldOneOfNumericConst(t *testing.T) {
	field := parseElicitationField("level", map[string]any{
		"type": "integer",
		"oneOf": []any{
			map[string]any{"const": float64(1), "title": "Low"},
			map[string]any{"const": float64(2), "title": "High"},
		},
	})
	if want := []string{"Low", "High"}; !reflect.DeepEqual(field.enum, want) {
		t.Fatalf("enum = %v, want %v", field.enum, want)
	}
	if want := []string{"1", "2"}; !reflect.DeepEqual(field.enumValues, want) {
		t.Fatalf("enumValues = %v, want %v", field.enumValues, want)
	}
}

func TestElicitationAnswersToContentOneOfLabelToValue(t *testing.T) {
	schema := acpsdk.UnstableElicitationSchema{
		Properties: map[string]any{
			"strategy": map[string]any{
				"type": "string",
				"oneOf": []any{
					map[string]any{"const": "safe", "title": "Safe mode"},
					map[string]any{"const": "fast", "title": "Fast mode"},
				},
			},
		},
		Required: []string{"strategy"},
	}
	content := elicitationAnswersToContent(map[string]string{"q_0": "Safe mode"}, schema)
	if content["strategy"] != "safe" {
		t.Fatalf("strategy = %#v, want const %q (not the display label)", content["strategy"], "safe")
	}
}

func TestElicitationAnswersToContentAnyOfLabelToValueMulti(t *testing.T) {
	schema := acpsdk.UnstableElicitationSchema{
		Properties: map[string]any{
			"tags": map[string]any{
				"type": "array",
				"items": map[string]any{
					"anyOf": []any{
						map[string]any{"const": "go", "title": "Go"},
						map[string]any{"const": "rust", "title": "Rust"},
					},
				},
			},
		},
		Required: []string{"tags"},
	}
	content := elicitationAnswersToContent(map[string]string{"q_0": "Go, Rust"}, schema)
	got, ok := content["tags"].([]string)
	if !ok || !reflect.DeepEqual(got, []string{"go", "rust"}) {
		t.Fatalf("tags = %#v, want [go rust]", content["tags"])
	}
}

func TestElicitationAnswersToContentOneOfUnknownFreeTextPassesThrough(t *testing.T) {
	schema := acpsdk.UnstableElicitationSchema{
		Properties: map[string]any{
			"strategy": map[string]any{
				"type":  "string",
				"oneOf": []any{map[string]any{"const": "safe", "title": "Safe mode"}},
			},
		},
		Required: []string{"strategy"},
	}
	content := elicitationAnswersToContent(map[string]string{"q_0": "whatever"}, schema)
	if content["strategy"] != "whatever" {
		t.Fatalf("strategy = %#v, want free text passed through", content["strategy"])
	}
}

// ===== D: duplicate question texts merge instead of overwrite =====

func TestBuildQuestionAnswersMergesDuplicateQuestionText(t *testing.T) {
	questions := []types.AskUserQuestionItem{
		{Question: "Same?"},
		{Question: "Same?"},
	}
	got := buildQuestionAnswers(questions, map[string]string{"q_0": "a", "q_1": "b"})
	want := map[string][]string{"Same?": {"a", "b"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestBuildQuestionAnswersMergeDeduplicates(t *testing.T) {
	questions := []types.AskUserQuestionItem{
		{Question: "Same?", MultiSelect: true, Options: []types.AskUserQuestionOption{{Label: "x"}, {Label: "y"}}},
		{Question: "Same?", MultiSelect: true, Options: []types.AskUserQuestionOption{{Label: "x"}, {Label: "y"}}},
	}
	got := buildQuestionAnswers(questions, map[string]string{"q_0": "x", "q_1": "x, y"})
	want := map[string][]string{"Same?": {"x", "y"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestBuildQuestionAnswersDistinctTextsUnchanged(t *testing.T) {
	got := buildQuestionAnswers(
		[]types.AskUserQuestionItem{{Question: "q1"}, {Question: "q2"}},
		map[string]string{"q_0": "a", "q_1": "b"},
	)
	want := map[string][]string{"q1": {"a"}, "q2": {"b"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// ===== E: message-only confirmation (empty schema) =====

func TestCreateElicitationEmptySchemaAndBlankMessageStillInvalid(t *testing.T) {
	proc := &Process{
		agentName:    "test-agent",
		sessions:     map[string]*sessionState{},
		sessionsByID: map[string]*sessionState{},
		elicitation:  newElicitationRegistry(),
	}
	client := &mindfsClient{proc: proc}
	resp, err := client.UnstableCreateElicitation(context.Background(), acpsdk.UnstableCreateElicitationRequest{
		Form: &acpsdk.UnstableCreateElicitationForm{
			Message:         "   ",
			RequestedSchema: acpsdk.UnstableElicitationSchema{Properties: map[string]any{}},
		},
	})
	if err == nil {
		t.Fatalf("blank message + empty schema must stay invalid_params, got resp %#v", resp)
	}
}

func TestCreateElicitationEmptySchemaWithMessageDeclinesNotInvalid(t *testing.T) {
	proc := &Process{
		agentName:    "test-agent",
		sessions:     map[string]*sessionState{},
		sessionsByID: map[string]*sessionState{},
		elicitation:  newElicitationRegistry(),
	}
	client := &mindfsClient{proc: proc}
	// No active session: the confirm question cannot be rendered, which must
	// surface as the decline variant rather than a JSON-RPC invalid_params.
	resp, err := client.UnstableCreateElicitation(context.Background(), acpsdk.UnstableCreateElicitationRequest{
		Form: &acpsdk.UnstableCreateElicitationForm{
			Message:         "May I proceed?",
			RequestedSchema: acpsdk.UnstableElicitationSchema{Properties: map[string]any{}},
		},
	})
	if err != nil {
		t.Fatalf("message-only confirm must not be rejected: %v", err)
	}
	if resp.Decline == nil {
		t.Fatalf("resp = %#v, want decline (no session to render)", resp)
	}
}

func TestCreateElicitationConfirmAcceptReturnsEmptyContent(t *testing.T) {
	proc, _, _ := newTestProcess(t, "key-1", "sess-1")
	client := &mindfsClient{proc: proc}

	done := make(chan acpsdk.UnstableCreateElicitationResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := client.UnstableCreateElicitation(context.Background(), acpsdk.UnstableCreateElicitationRequest{
			Form: &acpsdk.UnstableCreateElicitationForm{
				Message:         "May I proceed?",
				RequestedSchema: acpsdk.UnstableElicitationSchema{Properties: map[string]any{}},
			},
		})
		if err != nil {
			errCh <- err
			return
		}
		done <- resp
	}()
	id := waitForPendingElicitation(t, proc)
	if err := proc.answerElicitation(context.Background(), types.AskUserAnswer{
		ToolUseID: id,
		Answers:   map[string]string{"q_0": "Yes"},
	}); err != nil {
		t.Fatalf("answerElicitation: %v", err)
	}
	select {
	case resp := <-done:
		if resp.Accept == nil {
			t.Fatalf("resp = %#v, want accept variant for a Yes answer", resp)
		}
		if resp.Accept.Content == nil {
			t.Fatal("Accept.Content must be an empty map, got nil")
		}
		if len(resp.Accept.Content) != 0 {
			t.Fatalf("Accept.Content = %#v, want empty map (schema declares no fields)", resp.Accept.Content)
		}
	case err := <-errCh:
		t.Fatalf("UnstableCreateElicitation: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the confirm response")
	}
}

func TestCreateElicitationConfirmNoDeclines(t *testing.T) {
	proc, _, _ := newTestProcess(t, "key-1", "sess-1")
	client := &mindfsClient{proc: proc}

	done := make(chan acpsdk.UnstableCreateElicitationResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := client.UnstableCreateElicitation(context.Background(), acpsdk.UnstableCreateElicitationRequest{
			Form: &acpsdk.UnstableCreateElicitationForm{
				Message:         "May I proceed?",
				RequestedSchema: acpsdk.UnstableElicitationSchema{Properties: map[string]any{}},
			},
		})
		if err != nil {
			errCh <- err
			return
		}
		done <- resp
	}()
	id := waitForPendingElicitation(t, proc)
	if err := proc.answerElicitation(context.Background(), types.AskUserAnswer{
		ToolUseID: id,
		Answers:   map[string]string{"q_0": "No"},
	}); err != nil {
		t.Fatalf("answerElicitation: %v", err)
	}
	select {
	case resp := <-done:
		if resp.Decline == nil {
			t.Fatalf("resp = %#v, want decline variant for a No answer", resp)
		}
	case err := <-errCh:
		t.Fatalf("UnstableCreateElicitation: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the confirm response")
	}
}
