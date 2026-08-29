package model

import (
	"github.com/guregu/null/v6"
)

// Row — одна проводка листа ДДС в том виде, в каком она ложится в таблицу data.
//
// Значения справочников хранятся здесь именами, а не id: резолвом в FK занимается
// репозиторий, парсеру Google Sheets про идентификаторы знать незачем.
//
// Числа и даты — null-типы, потому что пустая ячейка означает NULL, а не ноль:
// debet/credit взаимоисключающие, как и sum_revenue/sum_cost/sum_return.
type Row struct {
	Date         null.Time
	NumOper      string
	TypeOper     string
	DebetVal     null.Float
	CreditVal    null.Float
	ExRate       null.Float
	Debet        null.Float
	Credit       null.Float
	Sender       string
	Description  string
	Bank         string
	Period       null.Time
	Organization string

	Division string
	Item     string
	SubItem  string
	FinType  string
	Vid      string

	Comment1 string
	Comment2 string

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
