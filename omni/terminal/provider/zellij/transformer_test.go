package zellij

import (
	"reflect"
	"testing"

	"github.com/Shaik-Sirajuddin/memory/terminal"
)

// TestRoundTrip verifies ToNative -> FromNative reconstructs the canonical
// Layout. The cases use the round-trippable subset that ToNativeWithFocus
// actually emits: panes carry explicit Command/Dir, start_suspended is only
// meaningful on command panes (renderPane omits it for bare panes), and
// layout-level Dir/tab Command are normalised into per-pane fields, so they
// are not exercised here.
func TestRoundTrip(t *testing.T) {
	tr := &ZellijTransformer{}

	cases := []struct {
		name   string
		layout terminal.Layout
	}{
		{
			name: "single tab single pane",
			layout: terminal.Layout{
				Tabs: []terminal.TabLayout{
					{
						Name: "main",
						Panes: []terminal.PaneLayout{
							{Command: "nvim", Dir: "/work/app", StartSuspended: true},
						},
					},
				},
			},
		},
		{
			name: "multiple tabs and panes",
			layout: terminal.Layout{
				Tabs: []terminal.TabLayout{
					{
						Name: "editor",
						Panes: []terminal.PaneLayout{
							{Command: "nvim .", Dir: "/work/app", StartSuspended: true},
						},
					},
					{
						Name: "shell",
						Panes: []terminal.PaneLayout{
							{Command: "bash", Dir: "/work/app", StartSuspended: false},
							{Command: "htop", Dir: "/tmp", StartSuspended: true},
						},
					},
				},
			},
		},
		{
			name: "tab name with spaces",
			layout: terminal.Layout{
				Tabs: []terminal.TabLayout{
					{
						Name: "my tab",
						Panes: []terminal.PaneLayout{
							{Command: "top", Dir: "/srv", StartSuspended: true},
						},
					},
				},
			},
		},
		{
			name: "bare pane no command",
			layout: terminal.Layout{
				Tabs: []terminal.TabLayout{
					{
						Name:  "blank",
						Panes: []terminal.PaneLayout{{}},
					},
				},
			},
		},
		{
			name: "command pane without dir",
			layout: terminal.Layout{
				Tabs: []terminal.TabLayout{
					{
						Name: "logs",
						Panes: []terminal.PaneLayout{
							{Command: "tail -f log", StartSuspended: true},
						},
					},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := tr.ToNative(tc.layout)
			if err != nil {
				t.Fatalf("ToNative: %v", err)
			}
			got, err := tr.FromNative(data)
			if err != nil {
				t.Fatalf("FromNative: %v", err)
			}
			if !reflect.DeepEqual(got, tc.layout) {
				t.Errorf("round-trip mismatch\n KDL:\n%s\n want: %#v\n got:  %#v", data, tc.layout, got)
			}
		})
	}
}
