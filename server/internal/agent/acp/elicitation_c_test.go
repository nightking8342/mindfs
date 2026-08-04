package acp

import (
	"reflect"
	"testing"

	"mindfs/server/internal/agent/types"

	acpsdk "github.com/coder/acp-go-sdk"
)

func TestSplitMultiValueWithOptionsSingleLabelWithComma(t *testing.T) {
	// Selecting a single label that itself contains a comma must not be split.
	got := splitMultiValueWithOptions("red, green", []string{"red, green", "blue"})
	want := []string{"red, green"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSplitMultiValueWithOptionsTwoCommaLabels(t *testing.T) {
	// Two selections whose labels contain commas are joined by ", "; the
	// longest-match reconstruction must recover both whole labels.
	got := splitMultiValueWithOptions("red, green, blue", []string{"red, green", "blue", "yellow"})
	want := []string{"red, green", "blue"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSplitMultiValueWithOptionsMirrorsOldBehavior(t *testing.T) {
	// When option labels contain no commas the result matches the old naive
	// split, so comma-free enums do not regress.
	got := splitMultiValueWithOptions("a, b, c", []string{"a", "b", "c"})
	old := splitMultiValue("a, b, c")
	if !reflect.DeepEqual(got, old) {
		t.Fatalf("with_options = %v, old = %v", got, old)
	}
}

func TestSplitMultiValueWithOptionsEmptyLabelsFallsBack(t *testing.T) {
	// No known labels: behave like the naive split for free-text input.
	got := splitMultiValueWithOptions("a, b,  c ,,d", nil)
	want := splitMultiValue("a, b,  c ,,d")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSplitMultiValueWithOptionsUnknownValueKept(t *testing.T) {
	// Free-text value that is not a known label is kept verbatim per fragment.
	got := splitMultiValueWithOptions("free, text", []string{"red, green", "blue"})
	want := []string{"free", "text"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBuildQuestionAnswersMultiSelectLabelsWithCommas(t *testing.T) {
	// The xAI path: buildQuestionAnswers must split by option labels, not by
	// every comma, so a comma-containing label survives intact.
	questions := []types.AskUserQuestionItem{
		{
			Question:    "Pick colors",
			MultiSelect: true,
			Options: []types.AskUserQuestionOption{
				{Label: "red, green"},
				{Label: "blue"},
				{Label: "yellow"},
			},
		},
	}
	// User selected "red, green" and "blue" -> frontend joined as "red, green, blue".
	answers := map[string]string{"q_0": "red, green, blue"}
	got := buildQuestionAnswers(questions, answers)
	want := map[string][]string{
		"Pick colors": {"red, green", "blue"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildQuestionAnswers = %#v, want %#v", got, want)
	}
}

func TestElicitationAnswersToContentMultiSelectLabelsWithCommas(t *testing.T) {
	// The standard elicitation path: split by enum labels so a
	// comma-containing enum value is not broken into fragments.
	schema := acpsdk.UnstableElicitationSchema{
		Properties: map[string]any{
			"colors": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string", "enum": []any{"red, green", "blue"}},
			},
		},
		Required: []string{"colors"},
	}
	answers := map[string]string{"q_0": "red, green, blue"}
	content := elicitationAnswersToContent(answers, schema)
	got, ok := content["colors"].([]string)
	if !ok {
		t.Fatalf("content[colors] = %#v, want []string", content["colors"])
	}
	want := []string{"red, green", "blue"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
