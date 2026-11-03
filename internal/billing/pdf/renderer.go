// Package pdf — PDF документов модуля расчётов (R-10, R-16): счёт на пополнение баланса,
// отчёт агента и акт на вознаграждение. Шрифты — встроенные шрифты Go (gofont, кириллица,
// лицензия BSD). Метаданные без персональных данных; время создания — из часов, поэтому
// одинаковые данные и часы дают одинаковые байты.
package pdf

import (
	"bytes"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-pdf/fpdf"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"

	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// msk — даты документов по Москве.
var msk = time.FixedZone("MSK", 3*60*60)

const (
	family    = "go"
	margin    = 15.0
	pageWidth = 210.0
	// contentWidth — ширина полосы набора A4.
	contentWidth = pageWidth - 2*margin
	lineHeight   = 5.0
)

// Renderer — реализация billing/domain.Renderer.
type Renderer struct {
	Clock clock.Clock
	Log   *slog.Logger
	// NoCompression — потоки без сжатия (тесты ищут текст в файле).
	NoCompression bool
}

var _ domain.Renderer = Renderer{}

// New создаёт генератор документов.
func New(clk clock.Clock, log *slog.Logger) Renderer { return Renderer{Clock: clk, Log: log} }

// document — начатый документ.
type document struct {
	*fpdf.Fpdf
}

// start — новый A4 с шрифтами и метаданными без персональных данных.
func (r Renderer) start(title string) document {
	f := fpdf.New("P", "mm", "A4", "")
	f.SetCompression(!r.NoCompression)
	f.SetCatalogSort(true)
	now := r.Clock.Now().UTC()
	f.SetCreationDate(now)
	f.SetModificationDate(now)
	f.SetCreator("maxim", false)
	f.SetProducer("maxim", false)
	f.SetTitle(title, true)
	f.AddUTF8FontFromBytes(family, "", goregular.TTF)
	f.AddUTF8FontFromBytes(family, "B", gobold.TTF)
	f.SetMargins(margin, margin, margin)
	f.SetAutoPageBreak(true, margin)
	f.AddPage()
	f.SetFont(family, "", 10)
	return document{f}
}

// finish — байты PDF и запись в журнал.
func (r Renderer) finish(d document, kind string) ([]byte, error) {
	var buf bytes.Buffer
	if err := d.Output(&buf); err != nil {
		return nil, fmt.Errorf("render %s pdf: %w", kind, err)
	}
	if r.Log != nil {
		r.Log.Debug("pdf rendered", logx.KeyOp, "Billing.RenderPDF", "document", kind, "bytes", buf.Len())
	}
	return buf.Bytes(), nil
}

// heading — заголовок документа.
func (d document) heading(text string) {
	d.SetFont(family, "B", 13)
	d.MultiCell(contentWidth, 7, text, "", "L", false)
	d.SetFont(family, "", 10)
	d.Ln(2)
}

// labelled — строка «Подпись: значение» с переносом длинного значения.
func (d document) labelled(label, value string) {
	const labelWidth = 32.0
	d.SetFont(family, "B", 10)
	y := d.GetY()
	d.CellFormat(labelWidth, lineHeight, label, "", 0, "L", false, 0, "")
	d.SetFont(family, "", 10)
	d.SetXY(margin+labelWidth, y)
	d.MultiCell(contentWidth-labelWidth, lineHeight, value, "", "L", false)
}

// paragraph — абзац с переносом.
func (d document) paragraph(text string) {
	d.MultiCell(contentWidth, lineHeight, text, "", "L", false)
}

// column — колонка таблицы.
type column struct {
	title string
	width float64
	align string
}

// table — таблица с шапкой; ячейки строк переносятся, высота строки — по самой высокой.
func (d document) table(cols []column, rows [][]string) {
	d.SetFont(family, "B", 9)
	d.row(cols, titles(cols), true)
	d.SetFont(family, "", 9)
	for _, r := range rows {
		d.row(cols, r, false)
	}
	d.SetFont(family, "", 10)
}

func titles(cols []column) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.title
	}
	return out
}

func (d document) row(cols []column, cells []string, head bool) {
	const cellLine = 4.5
	height := cellLine
	for i, c := range cols {
		if n := float64(len(d.SplitText(cells[i], c.width-2))); n*cellLine > height {
			height = n * cellLine
		}
	}
	_, pageHeight := d.GetPageSize()
	if d.GetY()+height > pageHeight-margin {
		d.AddPage()
	}
	x, y := d.GetX(), d.GetY()
	for i, c := range cols {
		d.Rect(x, y, c.width, height, "D")
		d.SetXY(x+1, y)
		align := c.align
		if head {
			align = "C"
		}
		d.MultiCell(c.width-2, cellLine, cells[i], "", align, false)
		x += c.width
	}
	d.SetXY(margin, y+height)
}

// signatures — строки подписей сторон.
func (d document) signatures(left, right string) {
	d.Ln(10)
	y := d.GetY()
	half := contentWidth / 2
	d.SetXY(margin, y)
	d.CellFormat(half, lineHeight, left+" ____________________", "", 0, "L", false, 0, "")
	if right != "" {
		d.SetXY(margin+half, y)
		d.CellFormat(half, lineHeight, right+" ____________________", "", 0, "L", false, 0, "")
	}
	d.Ln(lineHeight)
}

func dateText(t time.Time) string { return t.In(msk).Format("02.01.2006") }

var months = [...]string{"январь", "февраль", "март", "апрель", "май", "июнь", "июль", "август", "сентябрь",
	"октябрь", "ноябрь", "декабрь"}

// periodText — «октябрь 2026 г.».
func periodText(p domain.Period) string {
	s := p.Start()
	return fmt.Sprintf("%s %d г.", months[s.Month()-1], s.Year())
}

// supplierText — наименование, ИНН и ОГРНИП, адрес платформы.
func supplierText(s domain.Supplier) string {
	text := s.Name + ", ИНН " + s.INN
	if s.OGRNIP != "" {
		text += ", ОГРНИП " + s.OGRNIP
	}
	if s.Address != "" {
		text += ", " + s.Address
	}
	return text
}

// orgText — наименование и ИНН (КПП) организации.
func orgText(o domain.OrgRequisites) string {
	text := o.Name + ", ИНН " + o.INN
	if o.KPP != "" {
		text += ", КПП " + o.KPP
	}
	if o.Address != "" {
		text += ", " + o.Address
	}
	return text
}
