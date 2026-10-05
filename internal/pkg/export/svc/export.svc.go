// Package svc builds the Excel export of all transactions.
package svc

import (
	"bytes"
	"fmt"
	"sort"
	"time"

	"github.com/xuri/excelize/v2"

	"pieceomoney/internal/pkg/storage/repo"
)

const (
	sheetTx      = "Транзакции"
	sheetSummary = "По категориям"
)

// BuildXLSX returns a workbook with every transaction on one sheet (real
// numeric and date cells, filterable) and a category x month summary for
// defaultCurrency on another.
func BuildXLSX(txs []repo.Transaction, defaultCurrency string, loc *time.Location) ([]byte, error) {
	f := excelize.NewFile()
	defer f.Close()

	if err := f.SetSheetName("Sheet1", sheetTx); err != nil {
		return nil, err
	}
	if err := writeTransactions(f, txs, loc); err != nil {
		return nil, err
	}
	if _, err := f.NewSheet(sheetSummary); err != nil {
		return nil, err
	}
	if err := writeSummary(f, txs, defaultCurrency, loc); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeTransactions(f *excelize.File, txs []repo.Transaction, loc *time.Location) error {
	headers := []string{"Дата", "Время", "Сумма", "Валюта", "Магазин", "Карта", "Категория", "Описание"}
	if err := f.SetSheetRow(sheetTx, "A1", &[]any{headers[0], headers[1], headers[2], headers[3], headers[4], headers[5], headers[6], headers[7]}); err != nil {
		return err
	}

	for i, t := range txs {
		local := t.TS.In(loc)
		// Excel stores a date and a time as separate fractions of a day, so
		// two cells keep both individually sortable and filterable.
		day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
		clock := time.Date(1899, 12, 30, local.Hour(), local.Minute(), local.Second(), 0, time.UTC)
		row := i + 2
		err := f.SetSheetRow(sheetTx, fmt.Sprintf("A%d", row), &[]any{
			day, clock, float64(t.AmountMinor) / 100, t.Currency, t.Merchant, t.Card, t.Category, t.Name,
		})
		if err != nil {
			return err
		}
	}

	dateStyle, err := f.NewStyle(&excelize.Style{NumFmt: 14})
	if err != nil {
		return err
	}
	timeStyle, err := f.NewStyle(&excelize.Style{CustomNumFmt: strPtr("hh:mm")})
	if err != nil {
		return err
	}
	moneyStyle, err := f.NewStyle(&excelize.Style{NumFmt: 4})
	if err != nil {
		return err
	}
	headStyle, err := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "FFFFFF"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"2F5D8A"}, Pattern: 1},
		Alignment: &excelize.Alignment{Vertical: "center"},
	})
	if err != nil {
		return err
	}

	last := len(txs) + 1
	if err := f.SetCellStyle(sheetTx, "A1", "H1", headStyle); err != nil {
		return err
	}
	if len(txs) > 0 {
		if err := f.SetCellStyle(sheetTx, "A2", fmt.Sprintf("A%d", last), dateStyle); err != nil {
			return err
		}
		if err := f.SetCellStyle(sheetTx, "B2", fmt.Sprintf("B%d", last), timeStyle); err != nil {
			return err
		}
		if err := f.SetCellStyle(sheetTx, "C2", fmt.Sprintf("C%d", last), moneyStyle); err != nil {
			return err
		}
	}

	widths := map[string]float64{"A": 12, "B": 8, "C": 14, "D": 8, "E": 28, "F": 20, "G": 18, "H": 28}
	for col, w := range widths {
		if err := f.SetColWidth(sheetTx, col, col, w); err != nil {
			return err
		}
	}
	if err := f.SetPanes(sheetTx, &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"}); err != nil {
		return err
	}
	return f.AutoFilter(sheetTx, fmt.Sprintf("A1:H%d", max(last, 2)), nil)
}

func writeSummary(f *excelize.File, txs []repo.Transaction, currency string, loc *time.Location) error {
	months := map[string]bool{}
	byCat := map[string]map[string]int64{}
	for _, t := range txs {
		if t.Currency != currency {
			continue
		}
		month := t.TS.In(loc).Format("2006-01")
		cat := t.Category
		if cat == "" {
			cat = "Без категории"
		}
		months[month] = true
		if byCat[cat] == nil {
			byCat[cat] = map[string]int64{}
		}
		byCat[cat][month] += t.AmountMinor
	}

	monthList := make([]string, 0, len(months))
	for m := range months {
		monthList = append(monthList, m)
	}
	sort.Strings(monthList)
	cats := make([]string, 0, len(byCat))
	for c := range byCat {
		cats = append(cats, c)
	}
	sort.Strings(cats)

	header := []any{"Категория (" + currency + ")"}
	for _, m := range monthList {
		header = append(header, m)
	}
	header = append(header, "Всего")
	if err := f.SetSheetRow(sheetSummary, "A1", &header); err != nil {
		return err
	}

	colTotals := make([]int64, len(monthList))
	var grand int64
	for i, c := range cats {
		row := []any{c}
		var sum int64
		for j, m := range monthList {
			v := byCat[c][m]
			row = append(row, float64(v)/100)
			sum += v
			colTotals[j] += v
		}
		grand += sum
		row = append(row, float64(sum)/100)
		if err := f.SetSheetRow(sheetSummary, fmt.Sprintf("A%d", i+2), &row); err != nil {
			return err
		}
	}
	if len(cats) > 0 {
		totalRow := []any{"Итого"}
		for _, v := range colTotals {
			totalRow = append(totalRow, float64(v)/100)
		}
		totalRow = append(totalRow, float64(grand)/100)
		if err := f.SetSheetRow(sheetSummary, fmt.Sprintf("A%d", len(cats)+2), &totalRow); err != nil {
			return err
		}
	}

	endCol, err := excelize.ColumnNumberToName(len(monthList) + 2)
	if err != nil {
		return err
	}
	moneyStyle, err := f.NewStyle(&excelize.Style{NumFmt: 4})
	if err != nil {
		return err
	}
	boldStyle, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}, NumFmt: 4})
	if err != nil {
		return err
	}
	headStyle, err := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true, Color: "FFFFFF"},
		Fill: excelize.Fill{Type: "pattern", Color: []string{"2F5D8A"}, Pattern: 1},
	})
	if err != nil {
		return err
	}
	if err := f.SetCellStyle(sheetSummary, "A1", endCol+"1", headStyle); err != nil {
		return err
	}
	if len(cats) > 0 {
		if err := f.SetCellStyle(sheetSummary, "B2", fmt.Sprintf("%s%d", endCol, len(cats)+1), moneyStyle); err != nil {
			return err
		}
		if err := f.SetCellStyle(sheetSummary, fmt.Sprintf("A%d", len(cats)+2), fmt.Sprintf("%s%d", endCol, len(cats)+2), boldStyle); err != nil {
			return err
		}
	}
	return f.SetColWidth(sheetSummary, "A", "A", 24)
}

func strPtr(s string) *string { return &s }
