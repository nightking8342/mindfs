package acp

import (
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
)

var presetTestModelCat = acpsdk.SessionConfigOptionCategory("model")

func presetTestSelect(id acpsdk.SessionConfigId, category *acpsdk.SessionConfigOptionCategory, current string, options ...acpsdk.SessionConfigSelectOption) acpsdk.SessionConfigOption {
	ungrouped := acpsdk.SessionConfigSelectOptionsUngrouped(options)
	sel := acpsdk.SessionConfigOptionSelect{
		Id:           id,
		Name:         string(id),
		Category:     category,
		CurrentValue: acpsdk.SessionConfigValueId(current),
		Type:         "select",
		Options:      acpsdk.SessionConfigSelectOptions{Ungrouped: &ungrouped},
	}
	return acpsdk.SessionConfigOption{Select: &sel}
}

func strPtr(s string) *string { return &s }

func TestMapPresetConfigOptionsMatchesByIdWithoutCategory(t *testing.T) {
	// The DeepSeek Harness adapter emits the preset option as id "agent" with no
	// semantic category, unlike model/mode/thought_level options.
	selects := []acpsdk.SessionConfigOption{
		presetTestSelect("model", &presetTestModelCat, "deepseek-v4-flash",
			acpsdk.SessionConfigSelectOption{Value: "deepseek-v4-flash", Name: "DeepSeek V4 Flash"},
		),
		presetTestSelect(presetSessionConfigID, nil, "standard",
			acpsdk.SessionConfigSelectOption{Value: "standard", Name: "Standard"},
			acpsdk.SessionConfigSelectOption{Value: "wsl-cordis", Name: "WSL Creator"},
			acpsdk.SessionConfigSelectOption{Value: "minimal", Name: "Minimal", Description: strPtr("two tools")},
		),
	}

	list := mapPresetConfigOptions(selects)
	if list.CurrentPresetID != "standard" {
		t.Fatalf("CurrentPresetID = %q, want standard", list.CurrentPresetID)
	}
	if len(list.Presets) != 3 {
		t.Fatalf("len(presets) = %d, want 3", len(list.Presets))
	}
	if list.Presets[0].ID != "standard" || list.Presets[0].Name != "Standard" {
		t.Fatalf("preset[0] = %+v, want standard/Standard", list.Presets[0])
	}
	if list.Presets[2].Description != "two tools" {
		t.Fatalf("preset[2].Description = %q, want two tools", list.Presets[2].Description)
	}

	// Category lookup must NOT find the category-less agent option.
	if _, ok := findSelectConfigOption(selects, acpsdk.SessionConfigOptionCategory("agent")); ok {
		t.Fatal("category lookup should not match the category-less agent option")
	}
	// Id lookup must find it.
	if _, ok := findSelectConfigOptionByID(selects, presetSessionConfigID); !ok {
		t.Fatal("id lookup should match the agent option")
	}
}

func TestMapPresetConfigOptionsEmpty(t *testing.T) {
	selects := []acpsdk.SessionConfigOption{
		presetTestSelect("model", &presetTestModelCat, "m",
			acpsdk.SessionConfigSelectOption{Value: "m", Name: "M"},
		),
	}
	list := mapPresetConfigOptions(selects)
	if list.CurrentPresetID != "" || len(list.Presets) != 0 {
		t.Fatalf("expected empty preset list, got %+v", list)
	}
}

func TestFindPresetOptionByIDCarriesAgentID(t *testing.T) {
	// Guards the exact contract the DeepSeek Harness adapter uses: the preset
	// session config option is identified by id "agent" and carries the
	// currently selected preset in currentValue.
	options := []acpsdk.SessionConfigOption{
		presetTestSelect(presetSessionConfigID, nil, "standard",
			acpsdk.SessionConfigSelectOption{Value: "standard", Name: "Standard"},
			acpsdk.SessionConfigSelectOption{Value: "wsl-cordis", Name: "WSL"},
		),
	}
	opt, ok := findSelectConfigOptionByID(options, presetSessionConfigID)
	if !ok {
		t.Fatal("expected agent preset option present")
	}
	if opt.Id != "agent" {
		t.Fatalf("option id = %q, want agent", opt.Id)
	}
	if strings.TrimSpace(string(opt.CurrentValue)) != "standard" {
		t.Fatalf("current = %q, want standard", opt.CurrentValue)
	}
}