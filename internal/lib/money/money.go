// Package money форматирует денежные суммы для отчётов.
package money

import (
	"math/big"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
)

// nbsp — неразрывный пробел между разрядами: обычный позволил бы Telegram
// перенести строку посреди числа.
const nbsp = " "

// Format печатает NUMERIC как «1 234 567,89».
//
// Аргумент — pgtype.Numeric, а не float64: суммы считает Postgres точной
// десятичной арифметикой, и превращать результат в двоичную дробь ради вывода
// значило бы вносить погрешность ровно там, где её уже не было.
func Format(n pgtype.Numeric) string {
	if !n.Valid {
		return "—"
	}

	// Строковое представление NUMERIC — единственный способ добраться до цифр,
	// не потеряв масштаб: у Int/Exp он разъезжается на нулевых дробях.
	buf, err := n.MarshalJSON()
	if err != nil {
		return "—"
	}

	s := strings.Trim(string(buf), `"`)
	if s == "null" || s == "NaN" {
		return "—"
	}

	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}

	whole, frac, ok := strings.Cut(s, ".")
	if !ok {
		frac = ""
	}

	// Копейки показываются всегда: без выравнивания по два знака соседние
	// строки отчёта разъезжаются по ширине. Лишние знаки просто отсекаются —
	// суммируются колонки NUMERIC(17,2), масштаб суммы тоже 2, так что резать
	// тут нечего; округление понадобилось бы только на других данных.
	frac = (frac + "00")[:2]

	return sign + groups(whole) + "," + frac
}

// groups расставляет разделители разрядов справа налево.
func groups(s string) string {
	var b strings.Builder

	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteString(nbsp)
		}
		b.WriteRune(r)
	}

	return b.String()
}

// scale — масштаб, с которым копится сумма: те же две цифры после запятой,
// что и у денежных колонок NUMERIC(17,2).
const scale = -2

// Sum складывает суммы точно.
//
// Через big.Int, а не float64: значения приехали из NUMERIC, где они точны до
// копейки, и складывать их в двоичной дроби значило бы завести расхождение
// с выпиской ровно на последнем шаге. NULL и NaN пропускаются, пустой список
// даёт ноль — «итого 0,00» честнее прочерка, когда строк просто нет.
func Sum(values []pgtype.Numeric) pgtype.Numeric {
	total := new(big.Int)
	exp := int32(scale)

	for _, v := range values {
		if !v.Valid || v.NaN || v.Int == nil {
			continue
		}

		// Приведение к меньшему из показателей: домножать целое безопасно,
		// делить — значило бы терять младшие разряды.
		if v.Exp < exp {
			total.Mul(total, pow10(exp-v.Exp))
			exp = v.Exp
			total.Add(total, v.Int)

			continue
		}

		total.Add(total, new(big.Int).Mul(v.Int, pow10(v.Exp-exp)))
	}

	return pgtype.Numeric{Int: total, Exp: exp, Valid: true}
}

func pow10(n int32) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}
