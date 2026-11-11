package domain

import (
	"strings"
	"testing"
)

func TestParseChannelReference(t *testing.T) {
	for _, tc := range []struct{ input, kind, value string }{
		{"  Рецепты на каждый день  ", "title", "Рецепты на каждый день"},
		{"@recipes", "link", "https://max.ru/recipes"},
		{"https://max.ru/join/AbCd/", "link", "https://max.ru/join/AbCd"},
		{"http://MAX.RU/recipes/", "link", "https://max.ru/recipes"},
		{"-70000000001", "id", "-70000000001"},
		{"Канал 100%_", "title", "Канал 100%_"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			ref, err := ParseChannelReference(tc.input)
			if err != nil || ref.Kind != tc.kind || ref.Value != tc.value {
				t.Fatalf("%+v %v", ref, err)
			}
		})
	}
	for _, input := range []string{"", "  ", "@", "https://example.com/channel", "https://user:password@max.ru/channel", "https://max.ru/test?token=secret", "https://max.ru/test#fragment", "https://max.ru/", "line\nbreak", strings.Repeat("я", 513)} {
		if _, err := ParseChannelReference(input); err == nil {
			t.Errorf("invalid reference accepted: %q", input)
		}
	}
}
