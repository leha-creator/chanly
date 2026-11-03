package domain_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leha-creator/chanly/internal/post/domain"
)

var update = flag.Bool("update", false, "rewrite golden files in testdata")

func TestSerializeElements(t *testing.T) {
	h := func(nodes ...domain.Text) domain.Block {
		return domain.Block{Type: domain.BlockHeading, Content: nodes}
	}
	q := func(nodes ...domain.Text) domain.Block {
		return domain.Block{Type: domain.BlockBlockquote, Content: nodes}
	}
	tests := []struct {
		name string
		doc  domain.Doc
		want string
	}{
		{"plain", doc(p(txt("текст"))), "текст"},
		{"bold", doc(p(txt("a", bold))), "<b>a</b>"},
		{"italic", doc(p(txt("a", italic))), "<i>a</i>"},
		{"underline", doc(p(txt("a", domain.Mark{Type: domain.MarkUnderline}))), "<u>a</u>"},
		{"strike", doc(p(txt("a", domain.Mark{Type: domain.MarkStrike}))), "<s>a</s>"},
		{"code", doc(p(txt("a", code))), "<code>a</code>"},
		{"highlight", doc(p(txt("a", domain.Mark{Type: domain.MarkHighlight}))), "<mark>a</mark>"},
		{"link", doc(p(txt("a", link("https://a.test")))), `<a href="https://a.test">a</a>`},
		{"heading", doc(h(txt("H"))), "<h1>H</h1>"},
		{"blockquote", doc(q(txt("Q"))), "<blockquote>Q</blockquote>"},
		{"all marks nested in fixed order", doc(p(txt("a", link("https://a.test"), bold, italic,
			domain.Mark{Type: domain.MarkUnderline}, domain.Mark{Type: domain.MarkStrike}, domain.Mark{Type: domain.MarkHighlight}, code))),
			`<a href="https://a.test"><b><i><u><s><mark><code>a</code></mark></s></u></i></b></a>`},
		{"escaping in text", doc(p(txt(`a < b & "c" > d 'e'`))), `a &lt; b &amp; &quot;c&quot; &gt; d 'e'`},
		{"escaping in href", doc(p(txt("a", link(`https://a.test/?x=1&y="2"`)))), `<a href="https://a.test/?x=1&amp;y=&quot;2&quot;">a</a>`},
		{"heading and quote with marks", doc(h(txt("H", bold)), q(txt("Q", italic))), "<h1><b>H</b></h1>\n<blockquote><i>Q</i></blockquote>"},
		{"empty paragraph", doc(p(txt("a")), p(), p(txt("b"))), "a\n\nb"},
		{"line break inside text", doc(p(txt("a\nb"))), "a\nb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := domain.Serialize(tt.doc, nil); got != tt.want {
				t.Fatalf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
}

func TestSerializeUTM(t *testing.T) {
	utm := domain.UTMForOrder(42)
	all := "utm_source=max&utm_medium=channel_ad&utm_campaign=order_42"
	tests := []struct{ href, want string }{
		{"https://a.test", "https://a.test?" + all},
		{"https://a.test/p?z=1&a=2", "https://a.test/p?z=1&a=2&" + all},
		{"https://a.test/?utm_source=tg&x=1", "https://a.test/?utm_source=tg&x=1&utm_medium=channel_ad&utm_campaign=order_42"},
		{"https://a.test/p#frag", "https://a.test/p?" + all + "#frag"},
		{"https://a.test/p?x=1#frag", "https://a.test/p?x=1&" + all + "#frag"},
	}
	for _, tt := range tests {
		got := domain.Serialize(doc(p(txt("a", link(tt.href)))), &utm)
		want := `<a href="` + domain.EscapeHTML(tt.want) + `">a</a>`
		if got != want {
			t.Errorf("%s:\ngot  %s\nwant %s", tt.href, got, want)
		}
	}
	if domain.PreviewUTM.Campaign != "order_0000000000" {
		t.Fatalf("preview utm %q", domain.PreviewUTM.Campaign)
	}
}

func TestLength(t *testing.T) {
	for s, want := range map[string]int{
		"":           0,
		"abc":        3,
		"привет":     6,
		"😀":          2,
		"a😀б":        4,
		"<b>жир</b>": 10,
		"&amp;":      5,
	} {
		if got := domain.Length(s); got != want {
			t.Errorf("Length(%q) = %d, want %d", s, got, want)
		}
	}
	// Разметка и UTM-метки учитываются.
	d := doc(p(txt("ссылка", link("https://a.test"))))
	if domain.Length(domain.Serialize(d, &domain.PreviewUTM)) <= domain.Length(domain.Serialize(d, nil)) {
		t.Fatal("utm must increase length")
	}
}

// TestSerializeGolden сверяет сериализацию составных документов с эталонами
// testdata/serialize/*.golden; -update перезаписывает эталоны.
func TestSerializeGolden(t *testing.T) {
	utm := domain.UTMForOrder(1001)
	cases := map[string]domain.Doc{
		"post": doc(
			domain.Block{Type: domain.BlockHeading, Content: []domain.Text{txt("Осенняя распродажа")}},
			p(txt("Скидки до 50% — "), txt("только неделю", bold), txt(". Подробности: "),
				txt("https://shop.example.test/sale", link("https://shop.example.test/sale"))),
			p(),
			domain.Block{Type: domain.BlockBlockquote, Content: []domain.Text{txt("Цитата "), txt("с выделением", domain.Mark{Type: domain.MarkHighlight})}},
		),
		"special": doc(
			p(txt("<script>alert(\"x\")</script> & ещё")),
			p(txt("код", code, bold), txt(" и "), txt("ссылка с запросом", link("https://a.test/?q=1&r=2#top"), italic)),
		),
	}
	for name, d := range cases {
		t.Run(name, func(t *testing.T) {
			got := domain.Serialize(d, &utm)
			path := filepath.Join("testdata", "serialize", name+".golden")
			if *update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("golden %s: %v (run with -update)", path, err)
			}
			if got != strings.ReplaceAll(string(want), "\r\n", "\n") {
				t.Fatalf("got:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}
