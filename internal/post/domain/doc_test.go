package domain_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/leha-creator/chanly/internal/post/domain"
)

func p(nodes ...domain.Text) domain.Block {
	return domain.Block{Type: domain.BlockParagraph, Content: nodes}
}
func txt(s string, marks ...domain.Mark) domain.Text {
	return domain.Text{Text: s, Marks: marks}
}
func doc(blocks ...domain.Block) domain.Doc { return domain.Doc{Blocks: blocks} }

var (
	bold   = domain.Mark{Type: domain.MarkBold}
	italic = domain.Mark{Type: domain.MarkItalic}
	code   = domain.Mark{Type: domain.MarkCode}
)

func link(href string) domain.Mark { return domain.Mark{Type: domain.MarkLink, Href: href} }

func TestNormalizeRejects(t *testing.T) {
	lim := domain.DocLimits{MaxBlocks: 3, MaxNodes: 4, MaxJSONBytes: 1 << 10}
	tests := []struct {
		name string
		doc  domain.Doc
		path string
		rule string
	}{
		{"too many blocks", doc(p(), p(), p(), p()), "content", domain.RuleTooManyBlocks},
		{"too many nodes", doc(p(txt("a"), txt("b"), txt("c")), p(txt("d"), txt("e"))), "content", domain.RuleTooManyNodes},
		{"doc too large", doc(p(txt(strings.Repeat("я", 600)))), "content", domain.RuleDocTooLarge},
		{"unknown block", doc(p(), domain.Block{Type: "list"}), "content.1", domain.RuleInvalidBlock},
		{"unknown mark", doc(p(txt("a"), txt("b", domain.Mark{Type: "spoiler"}))), "content.0.content.1.marks.0", domain.RuleInvalidMark},
		{"link without href", doc(p(txt("a", bold, domain.Mark{Type: domain.MarkLink}))), "content.0.content.0.marks.1", domain.RuleHrefRequired},
		{"href on bold", doc(p(txt("a", domain.Mark{Type: domain.MarkBold, Href: "https://a.test"}))), "content.0.content.0.marks.0", domain.RuleHrefUnexpected},
		{"http link", doc(p(txt("a", link("http://a.test")))), "content.0.content.0.marks.0", domain.RuleHrefInvalid},
		{"no host", doc(p(txt("a", link("https:///path")))), "content.0.content.0.marks.0", domain.RuleHrefInvalid},
		{"userinfo", doc(p(txt("a", link("https://user:pw@a.test")))), "content.0.content.0.marks.0", domain.RuleHrefInvalid},
		{"space in href", doc(p(txt("a", link("https://a.test/x y")))), "content.0.content.0.marks.0", domain.RuleHrefInvalid},
		{"href too long", doc(p(txt("a", link("https://a.test/"+strings.Repeat("x", 2048))))), "content.0.content.0.marks.0", domain.RuleHrefInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := lim
			if tt.rule == domain.RuleHrefInvalid {
				l.MaxJSONBytes = 0 // длинный адрес не должен упереться в размер документа
			}
			_, err := domain.Normalize(tt.doc, l)
			var de *domain.DocError
			if !errors.As(err, &de) {
				t.Fatalf("want DocError, got %v", err)
			}
			if de.Path != tt.path || de.Rule != tt.rule {
				t.Fatalf("got %s %s, want %s %s", de.Path, de.Rule, tt.path, tt.rule)
			}
		})
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct {
		name string
		in   domain.Doc
		want domain.Doc
	}{
		{"line endings and tabs", doc(p(txt("a\r\nb\rc\td\x00e\u0007"))), doc(p(txt("a\nb\nc de")))},
		{"empty nodes removed", doc(p(txt(""), txt("a"), txt("", bold))), doc(p(txt("a")))},
		{"adjacent nodes merged", doc(p(txt("a", bold), txt("b", bold), txt("c"))), doc(p(txt("ab", bold), txt("c")))},
		{"duplicate marks removed and ordered", doc(p(txt("a", code, bold, bold, link("https://a.test"), link("https://b.test")))),
			doc(p(txt("a", link("https://a.test"), bold, code)))},
		{"merge after mark ordering", doc(p(txt("a", italic, bold), txt("b", bold, italic))), doc(p(txt("ab", bold, italic)))},
		{"empty paragraph kept", doc(p(txt("a")), p(), p(txt(""))), doc(p(txt("a")), p(), p())},
		{"heading and quote kept", doc(domain.Block{Type: domain.BlockHeading, Content: []domain.Text{txt("H")}},
			domain.Block{Type: domain.BlockBlockquote}),
			doc(domain.Block{Type: domain.BlockHeading, Content: []domain.Text{txt("H")}}, domain.Block{Type: domain.BlockBlockquote})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := domain.Normalize(tt.in, domain.DefaultDocLimits)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v\nwant %+v", got, tt.want)
			}
			again, err := domain.Normalize(got, domain.DefaultDocLimits)
			if err != nil || !reflect.DeepEqual(again, got) {
				t.Fatalf("normalize is not idempotent: %+v %v", again, err)
			}
		})
	}
}

func TestDocJSON(t *testing.T) {
	in := `{"type":"doc","content":[{"type":"heading","content":[{"type":"text","text":"H"}]},` +
		`{"type":"paragraph"},{"type":"paragraph","content":[{"type":"text","text":"a","marks":[{"type":"link","href":"https://a.test"}]}]}]}`
	var d domain.Doc
	if err := json.Unmarshal([]byte(in), &d); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != in {
		t.Fatalf("round trip:\n%s\n%s", out, in)
	}
	if d.EncodedSize() != len(in) {
		t.Fatalf("encoded size %d, want %d", d.EncodedSize(), len(in))
	}
	if err := json.Unmarshal([]byte(`{"type":"page","content":[]}`), &d); err == nil {
		t.Fatal("non-doc root accepted")
	}
}

func TestDocHelpers(t *testing.T) {
	d := doc(p(txt("a "), txt("ссылка", link("https://a.test"))), p(), p(txt("b", bold)))
	if !domain.HasLinks(d) || domain.HasLinks(doc(p(txt("a")))) {
		t.Fatal("HasLinks")
	}
	if got := domain.PlainText(d); got != "a ссылка\n\nb" {
		t.Fatalf("PlainText %q", got)
	}
	if got := domain.LinkHrefs(d); !reflect.DeepEqual(got, []string{"https://a.test"}) {
		t.Fatalf("LinkHrefs %v", got)
	}
}

func TestValidHref(t *testing.T) {
	for href, want := range map[string]bool{
		"https://a.test":                      true,
		"https://a.test/path?q=1#frag":        true,
		"https://ru.wikipedia.org/wiki/A_(B)": true,
		"http://a.test":                       false,
		"HTTPS://a.test":                      false,
		"https://":                            false,
		"https://u@a.test":                    false,
		"https://a.test/\u0001":               false,
		"https://a.test/ x":                   false,
	} {
		if got := domain.ValidHref(href); got != want {
			t.Errorf("ValidHref(%q) = %v, want %v", href, got, want)
		}
	}
}
