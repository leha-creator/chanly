package domain_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/leha-creator/chanly/internal/post/domain"
)

func TestAutolink(t *testing.T) {
	long := "https://a.test/" + strings.Repeat("x", 2048)
	tests := []struct {
		name  string
		in    domain.Doc
		want  domain.Doc
		found int
	}{
		{"start", doc(p(txt("https://a.test дальше"))),
			doc(p(txt("https://a.test", link("https://a.test")), txt(" дальше"))), 1},
		{"middle", doc(p(txt("см. https://a.test/x тут"))),
			doc(p(txt("см. "), txt("https://a.test/x", link("https://a.test/x")), txt(" тут"))), 1},
		{"end", doc(p(txt("см. https://a.test"))),
			doc(p(txt("см. "), txt("https://a.test", link("https://a.test")))), 1},
		{"trailing dot", doc(p(txt("см. https://a.test."))),
			doc(p(txt("см. "), txt("https://a.test", link("https://a.test")), txt("."))), 1},
		{"trailing comma", doc(p(txt("https://a.test, b"))),
			doc(p(txt("https://a.test", link("https://a.test")), txt(", b"))), 1},
		{"unpaired paren", doc(p(txt("(см. https://a.test)"))),
			doc(p(txt("(см. "), txt("https://a.test", link("https://a.test")), txt(")"))), 1},
		{"guillemet", doc(p(txt("«https://a.test»"))),
			doc(p(txt("«"), txt("https://a.test", link("https://a.test")), txt("»"))), 1},
		{"paired paren inside", doc(p(txt("https://ru.wikipedia.org/wiki/A_(B)."))),
			doc(p(txt("https://ru.wikipedia.org/wiki/A_(B)", link("https://ru.wikipedia.org/wiki/A_(B)")), txt("."))), 1},
		{"http not recognized", doc(p(txt("http://a.test"))), doc(p(txt("http://a.test"))), 0},
		{"inside word not recognized", doc(p(txt("xhttps://a.test"))), doc(p(txt("xhttps://a.test"))), 0},
		{"bold kept", doc(p(txt("жми https://a.test", bold))),
			doc(p(txt("жми ", bold), txt("https://a.test", link("https://a.test"), bold))), 1},
		{"existing link untouched", doc(p(txt("https://b.test", link("https://a.test")))),
			doc(p(txt("https://b.test", link("https://a.test")))), 0},
		{"two addresses", doc(p(txt("https://a.test и https://b.test"))),
			doc(p(txt("https://a.test", link("https://a.test")), txt(" и "), txt("https://b.test", link("https://b.test")))), 2},
		{"space after scheme", doc(p(txt("https:// a.test"))), doc(p(txt("https:// a.test"))), 0},
		{"too long stays text", doc(p(txt(long))), doc(p(txt(long))), 0},
		{"line break ends address", doc(p(txt("https://a.test\nдальше"))),
			doc(p(txt("https://a.test", link("https://a.test")), txt("\nдальше"))), 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, err := domain.Normalize(tt.in, domain.DocLimits{MaxBlocks: 300, MaxNodes: 3000})
			if err != nil {
				t.Fatal(err)
			}
			got, found := domain.Autolink(in)
			if !reflect.DeepEqual(got, tt.want) || found != tt.found {
				t.Fatalf("got %+v (%d)\nwant %+v (%d)", got, found, tt.want, tt.found)
			}
			// Повторное применение Normalize и Autolink ничего не меняет.
			norm, err := domain.Normalize(got, domain.DocLimits{MaxBlocks: 300, MaxNodes: 3000})
			if err != nil {
				t.Fatal(err)
			}
			again, n := domain.Autolink(norm)
			if !reflect.DeepEqual(again, got) || n != 0 {
				t.Fatalf("not idempotent: %+v (%d)", again, n)
			}
		})
	}
}
