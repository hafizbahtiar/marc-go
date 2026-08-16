package audit

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestDiffHanyaFieldBerubah(t *testing.T) {
	old := map[string]any{"content": "lama", "type": "normal"}
	new := map[string]any{"content": "baru", "type": "normal"}

	oldDelta, newDelta, changed := Diff(old, new)

	if !reflect.DeepEqual(changed, []string{"content"}) {
		t.Fatalf("changed = %v, mahu [content]", changed)
	}
	if !reflect.DeepEqual(oldDelta, map[string]any{"content": "lama"}) {
		t.Errorf("oldDelta = %v — 'type' tak berubah, tak patut disimpan", oldDelta)
	}
	if !reflect.DeepEqual(newDelta, map[string]any{"content": "baru"}) {
		t.Errorf("newDelta = %v", newDelta)
	}
}

func TestDiffTiadaPerubahan(t *testing.T) {
	m := map[string]any{"content": "sama"}
	oldDelta, newDelta, changed := Diff(m, map[string]any{"content": "sama"})
	if oldDelta != nil || newDelta != nil || changed != nil {
		t.Fatalf("mahu semua nil bila tiada perubahan, dapat %v/%v/%v", oldDelta, newDelta, changed)
	}
}

func TestDiffCreateDanDelete(t *testing.T) {
	snapshot := map[string]any{"content": "hai", "author_id": "abc"}

	// Create: tiada 'old' untuk dibanding — simpan 'new' penuh.
	oldDelta, newDelta, changed := Diff(nil, snapshot)
	if oldDelta != nil {
		t.Errorf("create patut tiada old_values, dapat %v", oldDelta)
	}
	if !reflect.DeepEqual(newDelta, snapshot) {
		t.Errorf("create newDelta = %v, mahu snapshot penuh", newDelta)
	}
	if !reflect.DeepEqual(changed, []string{"author_id", "content"}) {
		t.Errorf("changed = %v, mahu diisih", changed)
	}

	// Delete: sebaliknya.
	oldDelta, newDelta, _ = Diff(snapshot, nil)
	if newDelta != nil {
		t.Errorf("delete patut tiada new_values, dapat %v", newDelta)
	}
	if !reflect.DeepEqual(oldDelta, snapshot) {
		t.Errorf("delete oldDelta = %v, mahu snapshot penuh", oldDelta)
	}
}

// Field yang ditambah atau dibuang antara dua versi pun satu perubahan.
func TestDiffFieldDitambahDanDibuang(t *testing.T) {
	old := map[string]any{"a": 1}
	new := map[string]any{"b": 2}

	oldDelta, newDelta, changed := Diff(old, new)
	if !reflect.DeepEqual(changed, []string{"a", "b"}) {
		t.Fatalf("changed = %v, mahu [a b]", changed)
	}
	if oldDelta["b"] != nil {
		t.Errorf("field baharu patut nil di sebelah lama, dapat %v", oldDelta["b"])
	}
	if newDelta["a"] != nil {
		t.Errorf("field dibuang patut nil di sebelah baharu, dapat %v", newDelta["a"])
	}
}

func TestChangedFieldsSentiasaDiisih(t *testing.T) {
	old := map[string]any{"zulu": 1, "alpha": 1, "mike": 1}
	new := map[string]any{"zulu": 2, "alpha": 2, "mike": 2}
	for range 20 { // lelaran map rawak — ulang untuk pastikan stabil
		_, _, changed := Diff(old, new)
		if !reflect.DeepEqual(changed, []string{"alpha", "mike", "zulu"}) {
			t.Fatalf("changed = %v, mahu diisih", changed)
		}
	}
}

func TestMarshalOrNilKosongJadiNil(t *testing.T) {
	out, err := marshalOrNil(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if out != nil {
		t.Errorf("map kosong patut jadi NULL sql, dapat %q", out)
	}

	out, err = marshalOrNil(map[string]any{"a": 1})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("output bukan JSON sah: %v", err)
	}
}

func TestTruncateUserAgent(t *testing.T) {
	if got := truncate("abc", 10); got != "abc" {
		t.Errorf("truncate pendek = %q", got)
	}
	if got := truncate("abcdefghij", 4); got != "abcd" {
		t.Errorf("truncate panjang = %q, mahu abcd", got)
	}
}
