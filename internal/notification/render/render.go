// Package render — тексты уведомлений по шаблонам templates/notification (R-5, 07 §6):
// кабинет, личное сообщение бота MAX (Markdown) и письмо. Реализует notification/domain.Renderer.
package render

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"io/fs"
	"strings"
	"text/template"
	"unicode/utf8"

	"github.com/leha-creator/chanly/internal/notification/domain"
	"github.com/leha-creator/chanly/templates"
)

// MaxTextLength — предел личного сообщения бота.
// ПРЕДПОЛОЖЕНИЕ: тот же, что у сообщения канала (4000 символов, max.MaxTextLength, E-3).
const MaxTextLength = 4000

// Блоки файла шаблона.
const (
	blockTitle     = "title"
	blockText      = "text"
	blockEmailText = "email_text"
)

// Renderer собирает тексты уведомлений.
type Renderer struct {
	// cabinetURL — CABINET_BASE_URL без завершающей косой (R-17).
	cabinetURL string
	byCode     map[string]*template.Template
	email      *emailLayout
}

var _ domain.Renderer = (*Renderer)(nil)

type emailLayout struct {
	subject *template.Template
	text    *template.Template
	html    *htmltemplate.Template
}

// New разбирает шаблоны всех кодов каталога; кода без шаблона быть не может — ошибка запуска.
func New(cabinetURL string) (*Renderer, error) {
	r := &Renderer{cabinetURL: strings.TrimRight(cabinetURL, "/"), byCode: map[string]*template.Template{}}
	for _, t := range domain.Catalog() {
		src, err := fs.ReadFile(templates.FS, "notification/"+t.Code+".tmpl")
		if err != nil {
			return nil, fmt.Errorf("notification template %s: %w", t.Code, err)
		}
		tpl, err := template.New(t.Code).Funcs(funcs()).Option("missingkey=zero").Parse(string(src))
		if err != nil {
			return nil, fmt.Errorf("notification template %s: %w", t.Code, err)
		}
		for _, block := range []string{blockTitle, blockText} {
			if tpl.Lookup(block) == nil {
				return nil, fmt.Errorf("notification template %s: block %q is missing", t.Code, block)
			}
		}
		r.byCode[t.Code] = tpl
	}
	layout, err := loadEmailLayout()
	if err != nil {
		return nil, err
	}
	r.email = layout
	return r, nil
}

func loadEmailLayout() (*emailLayout, error) {
	read := func(part string) (string, error) {
		b, err := fs.ReadFile(templates.FS, "email/notification."+part+".tmpl")
		if err != nil {
			return "", fmt.Errorf("email layout %s: %w", part, err)
		}
		return string(b), nil
	}
	var l emailLayout
	src, err := read("subject")
	if err != nil {
		return nil, err
	}
	if l.subject, err = template.New("subject").Option("missingkey=error").Parse(src); err != nil {
		return nil, err
	}
	if src, err = read("txt"); err != nil {
		return nil, err
	}
	if l.text, err = template.New("text").Option("missingkey=error").Parse(src); err != nil {
		return nil, err
	}
	if src, err = read("html"); err != nil {
		return nil, err
	}
	if l.html, err = htmltemplate.New("html").Option("missingkey=error").Parse(src); err != nil {
		return nil, err
	}
	return &l, nil
}

// Cabinet — заголовок и текст для кабинета.
func (r *Renderer) Cabinet(n domain.Notification) (string, string, error) {
	return r.titleText(n, blockText, normalize(n.Payload))
}

// MAX — личное сообщение бота: заголовок жирным, текст, ссылка в кабинет; Markdown MAX.
// ПРЕДПОЛОЖЕНИЕ: format=markdown понимает **жирный** и [текст](url), спецсимволы в данных
// экранируются обратной косой (E-2).
func (r *Renderer) MAX(n domain.Notification) (string, error) {
	title, text, err := r.titleText(n, blockText, escapeMarkdown(normalize(n.Payload)))
	if err != nil {
		return "", err
	}
	var tail string
	if link := r.absLink(n); link != "" {
		tail = "\n\n[Открыть в кабинете](" + link + ")"
	}
	msg := "**" + title + "**\n\n" + text
	limit := MaxTextLength - utf8.RuneCountInString(tail)
	return truncate(msg, limit) + tail, nil
}

// Email — тема, текст и HTML письма по общему макету.
func (r *Renderer) Email(n domain.Notification) (string, string, string, error) {
	block := blockText
	if r.byCode[n.Template] != nil && r.byCode[n.Template].Lookup(blockEmailText) != nil {
		block = blockEmailText
	}
	title, text, err := r.titleText(n, block, normalize(n.Payload))
	if err != nil {
		return "", "", "", err
	}
	data := struct {
		Title      string
		Text       string
		Paragraphs []string
		Link       string
	}{Title: title, Text: text, Paragraphs: paragraphs(text), Link: r.absLink(n)}
	var b bytes.Buffer
	if err := r.email.subject.Execute(&b, data); err != nil {
		return "", "", "", err
	}
	subject := strings.TrimSpace(b.String())
	b.Reset()
	if err := r.email.text.Execute(&b, data); err != nil {
		return "", "", "", err
	}
	plain := b.String()
	b.Reset()
	if err := r.email.html.Execute(&b, data); err != nil {
		return "", "", "", err
	}
	return subject, plain, b.String(), nil
}

func (r *Renderer) titleText(n domain.Notification, textBlock string, data map[string]any) (string, string, error) {
	tpl, ok := r.byCode[n.Template]
	if !ok {
		return "", "", fmt.Errorf("%w: %q", domain.ErrUnknownTemplate, n.Template)
	}
	var b bytes.Buffer
	if err := tpl.ExecuteTemplate(&b, blockTitle, data); err != nil {
		return "", "", fmt.Errorf("render %s title: %w", n.Template, err)
	}
	title := oneLine(b.String())
	b.Reset()
	if err := tpl.ExecuteTemplate(&b, textBlock, data); err != nil {
		return "", "", fmt.Errorf("render %s text: %w", n.Template, err)
	}
	text := tidy(b.String())
	// missingkey=zero печатает отсутствующее значение как «<no value>»: такой текст не уходит.
	if strings.Contains(title+text, "<no value>") {
		return "", "", fmt.Errorf("render %s: %w", n.Template, ErrMissingData)
	}
	return title, text, nil
}

// ErrMissingData — в данных уведомления нет значения, которое шаблон выводит без проверки.
var ErrMissingData = errors.New("notification: template data is missing")

func (r *Renderer) absLink(n domain.Notification) string {
	link := n.Link()
	if link == "" || r.cabinetURL == "" {
		return ""
	}
	return r.cabinetURL + link
}

// normalize приводит данные к виду после jsonb: числа — json.Number, время — строка RFC 3339.
// Так текст одинаков при создании и при чтении из базы.
func normalize(p map[string]any) map[string]any {
	out := map[string]any{}
	raw, err := json.Marshal(p)
	if err != nil {
		return out
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	_ = dec.Decode(&out)
	return out
}

// markdownSpecial — символы разметки, которые экранируются в данных (E-2).
var markdownSpecial = strings.NewReplacer(`\`, `\\`, `*`, `\*`, `_`, `\_`, `[`, `\[`, `]`, `\]`, `(`, `\(`, `)`, `\)`, "`", "\\`", `~`, `\~`)

// escapeMarkdown экранирует строковые данные, кроме адресов (ключи link и *_url).
func escapeMarkdown(p map[string]any) map[string]any {
	for k, v := range p {
		s, ok := v.(string)
		if !ok || k == domain.DataLink || strings.HasSuffix(k, "_url") {
			continue
		}
		p[k] = markdownSpecial.Replace(s)
	}
	return p
}

// truncate обрезает текст до limit символов по границе строки или слова с «…» (E-3).
func truncate(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	runes := []rune(s)[:max(limit-1, 0)]
	cut := string(runes)
	if i := strings.LastIndexAny(cut, "\n "); i > len(cut)/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " \n") + "…"
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// tidy убирает отступы шаблона и лишние пустые строки.
func tidy(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	var out []string
	blank := false
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			if !blank && len(out) > 0 {
				out = append(out, "")
			}
			blank = true
			continue
		}
		blank = false
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

func paragraphs(s string) []string {
	var out []string
	for _, p := range strings.Split(s, "\n\n") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
