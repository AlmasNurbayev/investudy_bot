package model

import (
	"github.com/guregu/null/v6"
	"github.com/jackc/pgx/v5/pgtype"
)

// Row — одна проводка листа ДДС в том виде, в каком она ложится в таблицу data.
//
// Значения справочников хранятся здесь именами, а не id: резолвом в FK занимается
// репозиторий, парсеру Google Sheets про идентификаторы знать незачем.
//
// Все поля, которые ложатся в колонки data, — null-типы, потому что пустая
// ячейка означает NULL, а не ноль и не пустую строку: debet/credit
// взаимоисключающие, как и sum_revenue/sum_cost/sum_return, а пустой текст
// в TEXT-колонке заставил бы читающий код искать и NULL, и ”.
//
// Имена справочников — исключение и остаются обычными string: в data они не
// попадают вовсе, репозиторий меняет их на id, а пустое имя там и так означает
// NULL во внешнем ключе.
type Row struct {
	Date         null.Time
	NumOper      null.String
	TypeOper     null.String
	DebetVal     null.Float
	CreditVal    null.Float
	ExRate       null.Float
	Debet        null.Float
	Credit       null.Float
	Sender       null.String
	Description  null.String
	Bank         null.String
	Period       null.Time
	Organization null.String

	Division string
	Item     string
	SubItem  string
	FinType  string
	Vid      string

	Comment1 null.String
	Comment2 null.String

	SumDash    null.Float
	SumRevenue null.Float
	SumCost    null.Float
	SumReturn  null.Float
}

// Snapshot — версия среза данных.
type Snapshot struct {
	ID      int64
	TakenAt null.Time
	// RowCount заполняется в конце заливки, поэтому у недописанного среза он NULL.
	RowCount null.Int
	Year     int
	Month    int
	Week     int
}

// ReportRow — строка сводной таблицы: один разрез подразделение → статья →
// подстатья с итогами по дебету и кредиту.
//
// Разрезы приходят строками, а не id: отчёт их только печатает, а искать по
// ним нечего. Пустое значение справочника уже заменено прочерком в запросе.
//
// Суммы — pgtype.Numeric, а не null.Float, в отличие от Row: там числа приехали
// из листа через float64 и точнее уже не станут, а здесь их посчитал Postgres
// точной десятичной арифметикой, и переводить результат в двоичную дробь ради
// печати значило бы вносить погрешность на ровном месте.
type ReportRow struct {
	Division string
	Item     string
	SubItem  string
	Debet    pgtype.Numeric
	Credit   pgtype.Numeric
}

// ClosedReportsSettings — настройка отчёта из таблицы settings (ключ closed_reports).
type ClosedReportsSettings struct {
	// ExcludedItems — статьи, не попадающие в сводку: внутреннее движение
	// денег, которое иначе задваивает итоги.
	ExcludedItems []string `json:"excluded_items"`
}

// ClosedReport — готовый отчёт по закрытому периоду.
type ClosedReport struct {
	Title string
	// Snapshot — версия среза, по которой отчёт фактически посчитан.
	Snapshot Snapshot
	// Stale — использован не новейший срез: последняя загрузка оказалась
	// пустой. Читатель должен об этом знать: молча показанные вчерашние
	// данные выглядят как исправные сегодняшние.
	Stale       bool
	Rows        []ReportRow
	TotalDebet  pgtype.Numeric
	TotalCredit pgtype.Numeric
}
