package pdf

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/leha-creator/chanly/internal/platform/money"
)

var (
	unitsMale   = []string{"", "один", "два", "три", "четыре", "пять", "шесть", "семь", "восемь", "девять"}
	unitsFemale = []string{"", "одна", "две", "три", "четыре", "пять", "шесть", "семь", "восемь", "девять"}
	teens       = []string{"десять", "одиннадцать", "двенадцать", "тринадцать", "четырнадцать", "пятнадцать",
		"шестнадцать", "семнадцать", "восемнадцать", "девятнадцать"}
	tens     = []string{"", "", "двадцать", "тридцать", "сорок", "пятьдесят", "шестьдесят", "семьдесят", "восемьдесят", "девяносто"}
	hundreds = []string{"", "сто", "двести", "триста", "четыреста", "пятьсот", "шестьсот", "семьсот", "восемьсот", "девятьсот"}
)

// scale — разряд тысяч: формы слова и род.
type scale struct {
	forms  [3]string
	female bool
}

var scales = []scale{
	{forms: [3]string{"", "", ""}},
	{forms: [3]string{"тысяча", "тысячи", "тысяч"}, female: true},
	{forms: [3]string{"миллион", "миллиона", "миллионов"}},
	{forms: [3]string{"миллиард", "миллиарда", "миллиардов"}},
}

// plural — форма слова для числа n: 1 — [0], 2–4 — [1], прочее — [2].
func plural(n int64, forms [3]string) string {
	switch n100, n10 := n%100, n%10; {
	case n100 >= 11 && n100 <= 14:
		return forms[2]
	case n10 == 1:
		return forms[0]
	case n10 >= 2 && n10 <= 4:
		return forms[1]
	default:
		return forms[2]
	}
}

// triad — число 0–999 словами.
func triad(n int64, female bool) []string {
	var out []string
	if h := n / 100; h > 0 {
		out = append(out, hundreds[h])
	}
	switch rest := n % 100; {
	case rest >= 10 && rest < 20:
		out = append(out, teens[rest-10])
	default:
		if t := rest / 10; t > 0 {
			out = append(out, tens[t])
		}
		if u := rest % 10; u > 0 {
			if female {
				out = append(out, unitsFemale[u])
			} else {
				out = append(out, unitsMale[u])
			}
		}
	}
	return out
}

// integerWords — целое неотрицательное число словами, мужской род единиц.
func integerWords(n int64) string {
	if n == 0 {
		return "ноль"
	}
	var parts []string
	for i := len(scales) - 1; i >= 0; i-- {
		div := int64(1)
		for range i {
			div *= 1000
		}
		t := n / div % 1000
		if i == len(scales)-1 {
			t = n / div
		}
		if t == 0 {
			continue
		}
		parts = append(parts, triad(t, scales[i].female)...)
		if i > 0 {
			parts = append(parts, plural(t, scales[i].forms))
		}
	}
	return strings.Join(parts, " ")
}

// AmountInWords — сумма прописью для документов: «Одна тысяча двести рублей 05 копеек».
func AmountInWords(k money.Kopecks) string {
	rub, kop := int64(k)/100, int64(k)%100
	s := fmt.Sprintf("%s %s %02d %s", integerWords(rub), plural(rub, [3]string{"рубль", "рубля", "рублей"}),
		kop, plural(kop, [3]string{"копейка", "копейки", "копеек"}))
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[size:]
}

// FormatAmount — сумма цифрами с разделителем разрядов: «1 234 567,89».
func FormatAmount(k money.Kopecks) string {
	rub, kop := int64(k)/100, int64(k)%100
	digits := fmt.Sprint(rub)
	var b strings.Builder
	for i, c := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteRune(' ')
		}
		b.WriteRune(c)
	}
	return fmt.Sprintf("%s,%02d", b.String(), kop)
}
