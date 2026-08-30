package model

import (
	"github.com/guregu/null/v6"
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
