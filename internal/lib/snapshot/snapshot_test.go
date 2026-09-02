package snapshot

import (
	"errors"
	"testing"

	"github.com/guregu/null/v6"

	"investudy_bot/internal/model"
)

func snap(id int64, rows null.Int) model.Snapshot {
	return model.Snapshot{ID: id, RowCount: rows}
}

func TestLatestTakesNewest(t *testing.T) {
	got, err := Latest([]model.Snapshot{
		snap(3, null.IntFrom(70000)),
		snap(2, null.IntFrom(69000)),
	})
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if got.ID != 3 {
		t.Errorf("ID = %d, want 3", got.ID)
	}
}

// Главный случай: последний прогон записал версию без строк. Отчёт обязан
// откатиться на предыдущую, иначе пользователь увидит пустую таблицу и решит,
// что данные пропали.
func TestLatestSkipsEmptyAndUnfinished(t *testing.T) {
	got, err := Latest([]model.Snapshot{
		snap(5, null.Int{}),          // заливка не дописана
		snap(4, null.IntFrom(0)),     // пустой прогон
		snap(3, null.IntFrom(70000)), // рабочий
		snap(2, null.IntFrom(69000)),
	})
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if got.ID != 3 {
		t.Errorf("ID = %d, want 3", got.ID)
	}
}

func TestLatestWithoutUsableSnapshots(t *testing.T) {
	cases := map[string][]model.Snapshot{
		"пустой список": nil,
		"все пустые":    {snap(2, null.IntFrom(0)), snap(1, null.Int{})},
	}

	for name, snapshots := range cases {
		if _, err := Latest(snapshots); !errors.Is(err, ErrNoSnapshot) {
			t.Errorf("%s: err = %v, want ErrNoSnapshot", name, err)
		}
	}
}
