package output

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestMissingJQPaths(t *testing.T) {
	data := json.RawMessage(`{"campaign":{"id":9,"state":"draft"},"actions":[{"id":74}]}`)

	cases := []struct {
		expr string
		want []string
	}{
		{`{actions:[.campaign.actions[]?|{id}]}`, []string{".campaign.actions"}},
		{`.campaign["actions"]?`, []string{".campaign.actions"}},
		{`.campaign.actions`, nil},
		{`[.actions[] | .id]`, nil},
		{`.campaign.id[]?`, nil},
		{`.campaign.actions[]? // .actions[]`, nil},
		{`.nope[]? = 1`, nil},
		{`.nope[]? |= 1`, nil},
		{`select(.nope[]?)`, nil},
	}
	for _, c := range cases {
		t.Run(c.expr, func(t *testing.T) {
			if got := MissingJQPaths(data, c.expr); !slices.Equal(got, c.want) {
				t.Errorf("MissingJQPaths(%q) = %v, want %v", c.expr, got, c.want)
			}
		})
	}
}
