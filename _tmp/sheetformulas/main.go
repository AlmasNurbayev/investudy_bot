// Разовая утилита: выгружает формулы и значения всех листов таблицы в JSON.
// Лежит в _tmp — каталог исключён из git и контекста Docker.
package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/oauth2/jwt"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: sheetformulas <spreadsheetID> <outDir>")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(id, out string) error {
	raw, err := creds(".env")
	if err != nil {
		return err
	}

	var sa struct {
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
		TokenURI    string `json:"token_uri"`
	}
	if err = json.Unmarshal(raw, &sa); err != nil {
		return fmt.Errorf("parse credentials: %w", err)
	}
	if sa.TokenURI == "" {
		sa.TokenURI = "https://oauth2.googleapis.com/token"
	}
	fmt.Println("service account:", sa.ClientEmail)

	conf := &jwt.Config{
		Email:      sa.ClientEmail,
		PrivateKey: []byte(sa.PrivateKey),
		TokenURL:   sa.TokenURI,
		Scopes:     []string{"https://www.googleapis.com/auth/spreadsheets.readonly"},
	}
	client := conf.Client(context.Background())
	base := "https://sheets.googleapis.com/v4/spreadsheets/" + id

	var meta struct {
		Sheets []struct {
			Properties struct {
				Title string `json:"title"`
				Grid  struct {
					Rows int `json:"rowCount"`
					Cols int `json:"columnCount"`
				} `json:"gridProperties"`
			} `json:"properties"`
		} `json:"sheets"`
	}
	if err = get(client, base+"?fields=sheets.properties(title,gridProperties)", &meta); err != nil {
		return err
	}

	if err = os.MkdirAll(out, 0o755); err != nil {
		return err
	}

	for i, s := range meta.Sheets {
		title := s.Properties.Title
		fmt.Printf("%d\t%s\t%dx%d\n", i, title, s.Properties.Grid.Rows, s.Properties.Grid.Cols)

		rng := url.PathEscape("'" + strings.ReplaceAll(title, "'", "''") + "'")
		for _, render := range []string{"FORMULA", "FORMATTED_VALUE"} {
			var body json.RawMessage
			if err = get(client, fmt.Sprintf("%s/values/%s?valueRenderOption=%s", base, rng, render), &body); err != nil {
				return fmt.Errorf("sheet %q: %w", title, err)
			}
			name := filepath.Join(out, fmt.Sprintf("%02d_%s.json", i, strings.ToLower(render)))
			if err = os.WriteFile(name, body, 0o644); err != nil {
				return err
			}
		}
	}

	return nil
}

func get(c *http.Client, u string, v any) error {
	resp, err := c.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s: %s", u, resp.Status, body)
	}

	return json.Unmarshal(body, v)
}

// creds читает ключ из .env так же, как internal/sheets: GOOGLE_CREDENTIALS
// (JSON или base64) либо файл из GOOGLE_CREDENTIALS_FILE.
func creds(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		key, value, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `'"`)
		switch strings.TrimSpace(key) {
		case "GOOGLE_CREDENTIALS_FILE":
			if value != "" {
				return os.ReadFile(value)
			}
		case "GOOGLE_CREDENTIALS":
			if strings.HasPrefix(value, "{") {
				return []byte(value), nil
			}
			return base64.StdEncoding.DecodeString(value)
		}
	}

	return nil, fmt.Errorf("no credentials in %s", path)
}
