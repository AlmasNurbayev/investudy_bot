package sheets

import (
	"context"
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
	raw, err := os.ReadFile(cfg.CredentialsFile)
	if err != nil {
		return nil, fmt.Errorf("read credentials: %w", err)
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
	}, nil
}

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

	rows, skipped, err := parseRows(parsed.Values)
	if err != nil {
		return nil, err
	}

	if skipped > 0 {
		logger.DBG("sheet rows skipped", "reason", "empty date", "count", skipped)
	}

	return rows, nil
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
func parseRows(values [][]any) ([]model.Row, int, error) {
	rows := make([]model.Row, 0, len(values))
	skipped := 0

	// Суммы с длинной дробью (формулы в листе дают, например, 17,33333) округляет
	// Postgres при записи в NUMERIC(17,2) — точной десятичной арифметикой, точнее
	// чем это сделал бы Go на float64. Поэтому здесь только считаем такие значения,
	// а не трогаем: без счётчика их наличие в листе никак не заметить.
	overScale, example := 0, ""

	for i, raw := range values {
		if cell(raw, colDate) == "" {
			skipped++
			continue
		}

		for _, col := range moneyCols {
			if v := cell(raw, col); decimals(v) > moneyScale {
				overScale++
				if example == "" {
					example = fmt.Sprintf("строка %d: %q", i+firstDataRow, v)
				}
			}
		}

		row, err := parseRow(raw)
		if err != nil {
			// Номер строки — как в самом листе, чтобы ошибку можно было починить глазами.
			return nil, 0, fmt.Errorf("row %d: %w", i+firstDataRow, err)
		}

		rows = append(rows, row)
	}

	if overScale > 0 {
		logger.WRN("amounts rounded to 2 decimals",
			"count", overScale, "example", example)
	}

	return rows, skipped, nil
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

func parseRow(raw []any) (model.Row, error) {
	var err error

	get := func(col int) string { return cell(raw, col) }

	num := func(col int, name string) null.Float {
		if err != nil {
			return null.Float{}
		}
		v, e := parseNum(get(col))
		if e != nil {
			err = fmt.Errorf("%s: %w", name, e)
		}
		return v
	}

	date := func(col int, name string) null.Time {
		if err != nil {
			return null.Time{}
		}
		v, e := parseDate(get(col))
		if e != nil {
			err = fmt.Errorf("%s: %w", name, e)
		}
		return v
	}

	row := model.Row{
		Date:         date(colDate, "date"),
		NumOper:      get(colNumOper),
		TypeOper:     get(colTypeOper),
		DebetVal:     num(colDebetVal, "debet_val"),
		CreditVal:    num(colCreditVal, "credit_val"),
		ExRate:       num(colExRate, "ex_rate"),
		Debet:        num(colDebet, "debet"),
		Credit:       num(colCredit, "credit"),
		Sender:       get(colSender),
		Description:  get(colDescription),
		Bank:         get(colBank),
		Period:       date(colPeriod, "period"),
		Organization: get(colOrganization),

		Division: get(colDivision),
		Item:     get(colItem),
		SubItem:  get(colSubItem),
		FinType:  get(colFinType),
		Vid:      get(colVid),

		Comment1: get(colComment1),
		Comment2: get(colComment2),

		SumDash:    num(colSumDash, "sum_dash"),
		SumRevenue: num(colSumRevenue, "sum_revenue"),
		SumCost:    num(colSumCost, "sum_cost"),
		SumReturn:  num(colSumReturn, "sum_return"),
	}

	if err != nil {
		return model.Row{}, err
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

func parseDate(s string) (null.Time, error) {
	if s == "" {
		return null.Time{}, nil
	}

	v, err := time.Parse(dateLayout, s)
	if err != nil {
		return null.Time{}, fmt.Errorf("not a date %s: %q", dateLayout, s)
	}

	return null.TimeFrom(v), nil
}
