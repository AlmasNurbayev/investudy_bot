package sheets

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/guregu/null/v6"
	"golang.org/x/oauth2/jwt"

	"investudy_bot/internal/config"
	"investudy_bot/internal/logger"
	"investudy_bot/internal/model"
)

// Колонки листа ДДС, A..X. Порядок фиксирован структурой листа.
const (
	colDate = iota
	colNumOper
	colTypeOper
	colDebetVal
	colCreditVal
	colExRate
	colDebet
	colCredit
	colSender
	colDescription
	colBank
	colPeriod
	colOrganization
	colDivision
	colItem
	colSubItem
	colComment1
	colComment2
	colFinType
	colSumDash
	colVid
	colSumRevenue
	colSumCost
	colSumReturn
)

// firstDataRow — строка листа, с которой начинаются данные (1 — заголовок).
const firstDataRow = 2

// colTitles — заголовки колонок листа. Идут в сообщение администратору: искать
// ошибку он будет глазами в самом листе, где колонки подписаны именно так.
var colTitles = [...]string{
	colDate:         "Дата",
	colNumOper:      "#",
	colTypeOper:     "Тип",
	colDebetVal:     "Дебет валюта",
	colCreditVal:    "Кредит валюта",
	colExRate:       "Курс",
	colDebet:        "Дебет",
	colCredit:       "Кредит",
	colSender:       "Бенеф-р/отправитель",
	colDescription:  "Назначение платежа",
	colBank:         "Банк",
	colPeriod:       "Период",
	colOrganization: "Организация",
	colDivision:     "Подразделение",
	colItem:         "Статья",
	colSubItem:      "Подстатья",
	colComment1:     "Учет",
	colComment2:     "Комментарий",
	colFinType:      "Тип",
	colSumDash:      "СуммаДаш",
	colVid:          "Вид",
	colSumRevenue:   "СуммаДоход",
	colSumCost:      "СуммаРасход",
	colSumReturn:    "СуммаВозврат",
}

// cellAddr — адрес ячейки в нотации листа, например «W245». Колонок ровно 24,
// A..X, поэтому двухбуквенные адреса здесь не встречаются.
func cellAddr(col, row int) string {
	return fmt.Sprintf("%c%d", 'A'+col, row)
}

const (
	dateLayout      = "02.01.2006"
	readonlyScope   = "https://www.googleapis.com/auth/spreadsheets.readonly"
	apiBase         = "https://sheets.googleapis.com/v4/spreadsheets"
	defaultTokenURI = "https://oauth2.googleapis.com/token"
)

// Client читает лист через REST-эндпоинт Sheets API.
//
// Официальный SDK (google.golang.org/api/sheets/v4) сюда сознательно не взят: он
// поддерживает и gRPC-транспорт, и OpenTelemetry, и линкует их в бинарь целиком —
// 445 пакетов против 215, из них 64 grpc и 26 otel, при том что Sheets API чисто
// REST-ный. Нужен ровно один GET, поэтому берём http.Client с oauth2-транспортом.
type Client struct {
	http          *http.Client
	spreadsheetID string
	sheetName     string
	minPeriod     time.Time
}

// serviceAccount — та часть ключа сервис-аккаунта, которая нужна для JWT.
// Разбираем файл сами, а не через google.CredentialsFromJSON: та функция помечена
// deprecated именно за то, что принимает конфигурацию учётки без проверки формата.
type serviceAccount struct {
	Type        string `json:"type"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

func New(ctx context.Context, cfg config.SheetsConfig) (*Client, error) {
	raw, err := credentials(cfg)
	if err != nil {
		return nil, err
	}

	var sa serviceAccount
	if err = json.Unmarshal(raw, &sa); err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}

	switch {
	case sa.Type != "service_account":
		return nil, fmt.Errorf("credentials: expected service_account, got %q", sa.Type)
	case sa.ClientEmail == "":
		return nil, fmt.Errorf("credentials: client_email is empty")
	case sa.PrivateKey == "":
		return nil, fmt.Errorf("credentials: private_key is empty")
	}

	if sa.TokenURI == "" {
		sa.TokenURI = defaultTokenURI
	}

	conf := &jwt.Config{
		Email:      sa.ClientEmail,
		PrivateKey: []byte(sa.PrivateKey),
		TokenURL:   sa.TokenURI,
		Scopes:     []string{readonlyScope},
	}

	return &Client{
		http:          conf.Client(ctx),
		spreadsheetID: cfg.SpreadsheetID,
		sheetName:     cfg.SheetName,
		minPeriod:     cfg.MinPeriod.Time,
	}, nil
}

// credentials достаёт ключ сервис-аккаунта из того источника, который задан:
// значение переменной или файл. Что задан ровно один, проверил config.
//
// В переменной ключ лежит либо голым JSON, либо его base64. Различаем по первому
// символу, а не подбором: JSON ключа — всегда объект, а base64 объекта с него
// начаться не может. Гадание «сначала попробуем декодировать» на обрезанном
// значении дало бы мусор вместо внятного «parse credentials».
func credentials(cfg config.SheetsConfig) ([]byte, error) {
	if cfg.Credentials == "" {
		raw, err := os.ReadFile(cfg.CredentialsFile)
		if err != nil {
			return nil, fmt.Errorf("read credentials: %w", err)
		}

		return raw, nil
	}

	value := strings.TrimSpace(cfg.Credentials)
	if strings.HasPrefix(value, "{") {
		return []byte(value), nil
	}

	// base64 из разных источников приезжает по-разному: `base64` без -w0 рвёт
	// строку переносами, а часть инструментов отдаёт результат без выравнивания.
	// Ни то, ни другое не повод отказать — расхождение чисто в оформлении.
	value = stripSpace.Replace(value)

	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		if raw, err = base64.RawStdEncoding.DecodeString(value); err != nil {
			return nil, fmt.Errorf("decode credentials: expected service account JSON or its base64: %w", err)
		}
	}

	return raw, nil
}

var stripSpace = strings.NewReplacer("\n", "", "\r", "", " ", "", "\t", "")

type valuesResponse struct {
	Values [][]any `json:"values"`
}

// Fetch читает лист целиком и превращает строки в доменные структуры.
func (c *Client) Fetch(ctx context.Context) ([]model.Row, error) {
	// Имя листа берётся из конфига, поэтому экранируем апостроф удвоением.
	rng := fmt.Sprintf("'%s'!A%d:X", strings.ReplaceAll(c.sheetName, "'", "''"), firstDataRow)

	endpoint := fmt.Sprintf("%s/%s/values/%s",
		apiBase, url.PathEscape(c.spreadsheetID), url.PathEscape(rng))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", rng, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("get %s: %s: %s", rng, resp.Status, strings.TrimSpace(string(body)))
	}

	var parsed valuesResponse
	if err = json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	rows, st, err := parseRows(parsed.Values, parseOpts{
		sheet:     c.sheetName,
		minPeriod: c.minPeriod,
	})
	if err != nil {
		return nil, err
	}

	if st.noDate > 0 {
		logger.DBG("sheet rows skipped", "reason", "empty date", "count", st.noDate)
	}

	// Отсечённые по периоду считаются на уровне INF, а не DBG: это следствие
	// настройки, и её эффект должен быть виден в обычном логе прогона — иначе
	// съехавший MIN_PERIOD молча обрежет половину отчёта.
	if st.beforeMinPeriod > 0 {
		logger.INF("sheet rows skipped",
			"reason", "period before MIN_PERIOD",
			"count", st.beforeMinPeriod,
			"min_period", c.minPeriod.Format(dateLayout))
	}

	return rows, nil
}

// RowError — ошибка разбора строки листа вместе со всем, что нужно, чтобы найти
// её глазами: адрес ячейки, заголовок колонки, содержимое как есть и приметы
// самой строки — банк и организация.
//
// Тип экспортирован ради оповещения администратора: cmd/parser достаёт поля
// через errors.As и раскладывает их в сообщение. Внутри пакета он заполняется
// в два приёма — parseRow знает колонку и значение, parseRows добавляет то, что
// видно только на уровне листа.
type RowError struct {
	Sheet        string
	Row          int    // номер строки, как её нумерует Google Sheets
	Cell         string // адрес ячейки, например «W245»
	Column       string // заголовок колонки листа
	Value        string // содержимое ячейки как есть
	Want         string // чего в ячейке ждали, на языке листа
	Bank         string
	Organization string
	Err          error

	col int // индекс колонки: адрес не собрать, пока не известен номер строки
}

func (e *RowError) Error() string {
	return fmt.Sprintf("%s!%s (%s): %v", e.Sheet, e.Cell, e.Column, e.Err)
}

func (e *RowError) Unwrap() error { return e.Err }

// parseOpts — то, что парсер строк знает не из самих строк.
type parseOpts struct {
	sheet string
	// minPeriod — нижняя граница загрузки; нулевое значение отключает отсечку.
	minPeriod time.Time
}

// parseStats — сколько строк листа отброшено и почему.
type parseStats struct {
	noDate          int
	beforeMinPeriod int
}

// moneyScale — точность денежных колонок, та же, что у NUMERIC(17,2) в схеме.
const moneyScale = 2

// moneyCols — колонки с суммами. Курс сюда не входит: у него своя точность
// NUMERIC(17,8), и длинная дробь для него нормальна.
var moneyCols = []int{
	colDebetVal, colCreditVal, colDebet, colCredit,
	colSumDash, colSumRevenue, colSumCost, colSumReturn,
}

// parseRows отбирает содержательные строки листа.
//
// Признак действительности строки — заполненная дата. Пустая дата означает
// разделитель, итоговую строку или хвост листа, поэтому такие строки отбрасываются
// до разбора остальных колонок: в них может лежать что угодно, и попытка их
// прочитать завалила бы синк целиком.
func parseRows(values [][]any, opts parseOpts) ([]model.Row, parseStats, error) {
	rows := make([]model.Row, 0, len(values))

	var st parseStats

	// Суммы с длинной дробью (формулы в листе дают, например, 17,33333) округляет
	// Postgres при записи в NUMERIC(17,2) — точной десятичной арифметикой, точнее
	// чем это сделал бы Go на float64. Поэтому здесь только считаем такие значения,
	// а не трогаем: без счётчика их наличие в листе никак не заметить.
	overScale, example := 0, ""

	for i, raw := range values {
		rowNum := i + firstDataRow

		// Номер строки — как в самом листе: ошибку чинить придётся глазами.
		fail := func(re *RowError) ([]model.Row, parseStats, error) {
			re.Sheet = opts.sheet
			re.Row = rowNum
			re.Cell = cellAddr(re.col, rowNum)
			re.Bank = cell(raw, colBank)
			re.Organization = cell(raw, colOrganization)

			return nil, parseStats{}, re
		}

		if cell(raw, colDate) == "" {
			st.noDate++
			continue
		}

		// Отсечка по периоду идёт до разбора остальных колонок, и намеренно:
		// иначе опечатка в сумме десятилетней давности роняла бы синк из-за
		// строки, которую мы и не собирались грузить. Заодно в справочники не
		// попадают значения, встречающиеся только в старых строках.
		//
		// Строка без периода остаётся: доказать, что она старая, нечем, а тихо
		// выбросить проводку хуже, чем загрузить лишнюю.
		if !opts.minPeriod.IsZero() {
			period, err := parseDate(cell(raw, colPeriod))
			if err != nil {
				return fail(cellFault(raw, colPeriod, wantDate, err))
			}

			if period.Valid && period.Time.Before(opts.minPeriod) {
				st.beforeMinPeriod++
				continue
			}
		}

		for _, col := range moneyCols {
			if v := cell(raw, col); decimals(v) > moneyScale {
				overScale++
				if example == "" {
					example = fmt.Sprintf("строка %d: %q", rowNum, v)
				}
			}
		}

		row, re := parseRow(raw)
		if re != nil {
			return fail(re)
		}

		rows = append(rows, row)
	}

	if overScale > 0 {
		logger.WRN("amounts rounded to 2 decimals",
			"count", overScale, "example", example)
	}

	return rows, st, nil
}

// Чего ждали в ячейке — формулировки для администратора, на языке листа.
const (
	wantNumber = "число"
	wantDate   = "дата в формате ДД.ММ.ГГГГ"
)

// cellFault собирает ту часть RowError, которая видна по одной ячейке.
func cellFault(raw []any, col int, want string, err error) *RowError {
	return &RowError{
		Column: colTitles[col],
		Value:  cell(raw, col),
		Want:   want,
		Err:    err,
		col:    col,
	}
}

// decimals возвращает число знаков после десятичного разделителя.
func decimals(s string) int {
	if i := strings.IndexAny(s, ".,"); i >= 0 {
		return len(s) - i - 1
	}

	return 0
}

// cell возвращает ячейку строки. Google Sheets обрезает хвостовые пустые ячейки,
// поэтому строка бывает короче объявленных 24 колонок.
func cell(raw []any, col int) string {
	if col >= len(raw) || raw[col] == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(raw[col]))
}

// parseRow разбирает одну строку листа. Ошибка возвращается конкретным типом,
// а не interface error: заполнить её до конца может только parseRows, которому
// известны номер строки и её приметы.
//
// Первая же испорченная ячейка останавливает разбор строки: остальные значения
// в такой строке всё равно никуда не поедут, а сообщать администратору нужно
// про одну ячейку, а не про список.
func parseRow(raw []any) (model.Row, *RowError) {
	var fault *RowError

	get := func(col int) string { return cell(raw, col) }

	// Пустая ячейка — это NULL, а не пустая строка: иначе читающему коду
	// пришлось бы проверять и IS NULL, и = '' в каждом запросе.
	text := func(col int) null.String {
		v := get(col)
		return null.NewString(v, v != "")
	}

	num := func(col int) null.Float {
		if fault != nil {
			return null.Float{}
		}
		v, e := parseNum(get(col))
		if e != nil {
			fault = cellFault(raw, col, wantNumber, e)
		}
		return v
	}

	date := func(col int) null.Time {
		if fault != nil {
			return null.Time{}
		}
		v, e := parseDate(get(col))
		if e != nil {
			fault = cellFault(raw, col, wantDate, e)
		}
		return v
	}

	row := model.Row{
		Date:         date(colDate),
		NumOper:      text(colNumOper),
		TypeOper:     text(colTypeOper),
		DebetVal:     num(colDebetVal),
		CreditVal:    num(colCreditVal),
		ExRate:       num(colExRate),
		Debet:        num(colDebet),
		Credit:       num(colCredit),
		Sender:       text(colSender),
		Description:  text(colDescription),
		Bank:         text(colBank),
		Period:       date(colPeriod),
		Organization: text(colOrganization),

		Division: get(colDivision),
		Item:     get(colItem),
		SubItem:  get(colSubItem),
		FinType:  get(colFinType),
		Vid:      get(colVid),

		Comment1: text(colComment1),
		Comment2: text(colComment2),

		SumDash:    num(colSumDash),
		SumRevenue: num(colSumRevenue),
		SumCost:    num(colSumCost),
		SumReturn:  num(colSumReturn),
	}

	if fault != nil {
		return model.Row{}, fault
	}

	return row, nil
}

// parseNum разбирает числа в формате листа: «46 829,00» — запятая как десятичный
// разделитель, пробелы (в том числе неразрывные) как разделитель разрядов.
var numCleaner = strings.NewReplacer(
	" ", "",
	" ", "", // неразрывный пробел
	" ", "", // узкий неразрывный пробел
	",", ".",
)

func parseNum(s string) (null.Float, error) {
	if s == "" {
		return null.Float{}, nil
	}

	v, err := strconv.ParseFloat(numCleaner.Replace(s), 64)
	if err != nil {
		return null.Float{}, fmt.Errorf("not a number: %q", s)
	}

	return null.FloatFrom(v), nil
}

// parseDate разбирает дату листа, отбрасывая время, если оно есть.
//
// Значения читаются как FORMATTED_VALUE, то есть ровно так, как ячейка выглядит
// в листе, а формат задаётся поячеечно. Поэтому в одной колонке соседствуют
// «01.08.2026» и «01.08.2026 12:00:00» — вторая ячейка отформатирована как дата
// со временем. Строгий разбор ронял бы на ней весь синк («extra text»).
//
// Время отбрасывается, а не приводится к 00:00:00: в доменной модели его нет,
// колонки date и period в схеме — DATE, и Postgres хранит только календарный
// день. Отбрасывание здесь лишь избавляет от падения на входе.
func parseDate(s string) (null.Time, error) {
	if s == "" {
		return null.Time{}, nil
	}

	// Fields режет по unicode.IsSpace, поэтому неразрывный пробел тоже считается.
	day := s
	if f := strings.Fields(s); len(f) > 0 {
		day = f[0]
	}

	v, err := time.Parse(dateLayout, day)
	if err != nil {
		// В сообщении исходная ячейка целиком: чинить в листе придётся именно её.
		return null.Time{}, fmt.Errorf("not a date %s: %q", dateLayout, s)
	}

	return null.TimeFrom(v), nil
}
