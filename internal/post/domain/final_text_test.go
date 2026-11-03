package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/leha-creator/chanly/internal/post/domain"
)

func TestFinalText(t *testing.T) {
	const (
		name = "ООО «Ромашка»"
		inn  = "7707083893"
		erid = "2VtzqvXYZ"
	)
	withLink := `Текст <a href="https://a.test/?x=1&amp;y=2">ссылка</a> и <a href="https://b.test#f">ещё</a>`
	noLink := "Текст без ссылок"
	label := "Реклама. ООО «Ромашка», ИНН 7707083893."
	tests := []struct {
		name     string
		body     string
		carrier  domain.TokenCarrier
		position domain.TokenPosition
		want     string
	}{
		{"text below", noLink, domain.CarrierText, domain.PositionBelow, noLink + "\n\n" + label + " erid: " + erid},
		{"text above", noLink, domain.CarrierText, domain.PositionAbove, label + " erid: " + erid + "\n" + noLink},
		{"text link", withLink, domain.CarrierText, domain.PositionLink,
			`Текст <a href="https://a.test/?x=1&amp;y=2&amp;erid=2VtzqvXYZ">ссылка</a> и <a href="https://b.test?erid=2VtzqvXYZ#f">ещё</a>` +
				"\n\n" + label},
		{"media below", noLink, domain.CarrierMedia, domain.PositionBelow, noLink + "\n\nerid: " + erid},
		{"media above", noLink, domain.CarrierMedia, domain.PositionAbove, "erid: " + erid + "\n" + noLink},
		{"media link: no marking line", withLink, domain.CarrierMedia, domain.PositionLink,
			`Текст <a href="https://a.test/?x=1&amp;y=2&amp;erid=2VtzqvXYZ">ссылка</a> и <a href="https://b.test?erid=2VtzqvXYZ#f">ещё</a>`},
		{"text below with links", withLink, domain.CarrierText, domain.PositionBelow, withLink + "\n\n" + label + " erid: " + erid},
		{"media above with links", withLink, domain.CarrierMedia, domain.PositionAbove, "erid: " + erid + "\n" + withLink},
		{"text link without links", noLink, domain.CarrierText, domain.PositionLink, noLink + "\n\n" + label},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := domain.FinalText(tt.body, domain.Marking{Carrier: tt.carrier, Position: tt.position, Name: name, INN: inn},
				erid, domain.DefaultFinalTextLimit)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func TestMarkingLineEscapes(t *testing.T) {
	got := domain.MarkingLine(domain.Marking{Carrier: domain.CarrierText, Position: domain.PositionLink, Name: `A & B <"C">`, INN: "1"}, "e")
	if got != "Реклама. A &amp; B &lt;&quot;C&quot;&gt;, ИНН 1." {
		t.Fatalf("got %q", got)
	}
}

func TestFinalTextLimit(t *testing.T) {
	// Резерв 200 знаков под строку маркировки (A-38) вмещает название до 143 знаков
	// при ИНН из 12 цифр и erid из 20 символов: «Реклама. » + название + «, ИНН » + 12 + «.»
	// + « erid: » + 20 + пустая строка = 57 + название.
	body := strings.Repeat("я", domain.DefaultBodyLimit)
	m := domain.Marking{Carrier: domain.CarrierText, Position: domain.PositionBelow, INN: "500100732259"}
	erid := strings.Repeat("e", 20)

	m.Name = strings.Repeat("н", 143)
	got, err := domain.FinalText(body, m, erid, domain.DefaultFinalTextLimit)
	if err != nil || domain.Length(got) != domain.DefaultFinalTextLimit {
		t.Fatalf("143-char name: length %d, err %v", domain.Length(got), err)
	}
	m.Name = strings.Repeat("н", 144)
	if _, err := domain.FinalText(body, m, erid, domain.DefaultFinalTextLimit); !errors.Is(err, domain.ErrFinalTextTooLong) {
		t.Fatalf("144-char name: want ErrFinalTextTooLong, got %v", err)
	}
	// Длинное название организации (до 500 знаков) не помещается: это ловит MarkReady.
	m.Name = strings.Repeat("н", 200)
	if _, err := domain.FinalText(body, m, erid, domain.DefaultFinalTextLimit); !errors.Is(err, domain.ErrFinalTextTooLong) {
		t.Fatalf("200-char name: want ErrFinalTextTooLong, got %v", err)
	}
}

func TestVerifyFinalText(t *testing.T) {
	body := `Текст <a href="https://a.test/">ссылка</a>`
	for _, carrier := range []domain.TokenCarrier{domain.CarrierText, domain.CarrierMedia} {
		for _, position := range []domain.TokenPosition{domain.PositionAbove, domain.PositionBelow, domain.PositionLink} {
			v := domain.Version{Status: domain.VersionApproved, Body: body, TokenCarrier: carrier, TokenPosition: position,
				AdvertiserName: "ООО «Ромашка»", AdvertiserINN: "7707083893"}
			if err := v.MarkReady("2VtzqvXYZ", domain.DefaultFinalTextLimit); err != nil {
				t.Fatal(err)
			}
			if err := domain.VerifyFinalText(v, domain.DefaultFinalTextLimit); err != nil {
				t.Fatalf("%s/%s: %v", carrier, position, err)
			}
			forged := v
			forged.FinalText = strings.ReplaceAll(v.FinalText, "2VtzqvXYZ", "OTHER")
			if forged.FinalText == v.FinalText {
				forged.FinalText += " "
			}
			if err := domain.VerifyFinalText(forged, domain.DefaultFinalTextLimit); !errors.Is(err, domain.ErrMarkingMismatch) {
				t.Fatalf("%s/%s forged text: %v", carrier, position, err)
			}
			otherErid := v
			otherErid.Erid = "OTHER"
			if err := domain.VerifyFinalText(otherErid, domain.DefaultFinalTextLimit); !errors.Is(err, domain.ErrMarkingMismatch) {
				t.Fatalf("%s/%s other erid: %v", carrier, position, err)
			}
		}
	}
	notReady := domain.Version{Status: domain.VersionApproved, Body: body}
	if err := domain.VerifyFinalText(notReady, domain.DefaultFinalTextLimit); !errors.Is(err, domain.ErrMarkingMismatch) {
		t.Fatalf("approved version: %v", err)
	}
}
