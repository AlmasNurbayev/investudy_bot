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
	sign, whole, frac, ok := digits(n)
	if !ok {
		return "—"
	}

	return sign + groups(whole) + "," + frac
}

// Decimal печатает NUMERIC десятичной строкой для JSON: «-1234567.89».
//
// Строкой, а не числом: NUMERIC(17,2) упирается в границу точности Number
// в JavaScript, и сумма, прошедшая через JSON-число, могла бы потерять
// копейки молча. Фронт форматирует строку сам и парсит её только для графиков.
// NULL — пустая строка: «не считалось» не должно выглядеть нулём.
func Decimal(n pgtype.Numeric) string {
	sign, whole, frac, ok := digits(n)
	if !ok {
		return ""
	}

	return sign + whole + "." + frac
}

// digits раскладывает NUMERIC на знак, целую часть и ровно две цифры дроби.
// ok = false для NULL и NaN.
func digits(n pgtype.Numeric) (sign, whole, frac string, ok bool) {
	if !n.Valid {
		return "", "", "", false
	}

	// Строковое представление NUMERIC — единственный способ добраться до цифр,
	// не потеряв масштаб: у Int/Exp он разъезжается на нулевых дробях.
	buf, err := n.MarshalJSON()
	if err != nil {
		return "", "", "", false
	}

	s := strings.Trim(string(buf), `"`)
	if s == "null" || s == "NaN" {
		return "", "", "", false
	}

	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}

	whole, frac, found := strings.Cut(s, ".")
	if !found {
		frac = ""
	}

	// Копейки показываются всегда: без выравнивания по два знака соседние
	// строки отчёта разъезжаются по ширине. Лишние знаки просто отсекаются —
	// суммируются колонки NUMERIC(17,2), масштаб суммы тоже 2, так что резать
	// тут нечего; округление понадобилось бы только на других данных.
	return sign, whole, (frac + "00")[:2], true
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

// Add складывает две суммы точно — Sum для пары. NULL считается нулём.
func Add(a, b pgtype.Numeric) pgtype.Numeric {
	return Sum([]pgtype.Numeric{a, b})
}

func pow10(n int32) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}
