package sitefile

import (
	"os"
	"strings"
	"testing"
)

func TestHistoryKeepsTheLastVersions(t *testing.T) {
	st, dir := newStore(t)
	if err := st.Snapshot("main.conf", nil, "ada"); err != nil {
		t.Fatal(err)
	}
	if list, _ := st.History("main.conf"); len(list) != 0 {
		t.Fatalf("an empty text is not a version: %+v", list)
	}
	for i := 0; i < HistoryKeep+5; i++ {
		if err := st.Snapshot("main.conf", []byte("version "+string(rune('A'+i%26))+strings.Repeat("x", i)), "Ada Lovelace/../x"); err != nil {
			t.Fatal(err)
		}
	}
	list, err := st.History("main.conf")
	if err != nil || len(list) != HistoryKeep {
		t.Fatalf("kept %d versions (%v), want %d", len(list), err, HistoryKeep)
	}
	if list[0].ID < list[1].ID {
		t.Error("newest first")
	}
	if list[0].Who != "AdaLovelace..x" { // characters outside letters, digits . - _ are dropped
		t.Errorf("who = %q", list[0].Who)
	}
	// The same text twice in a row keeps one.
	before := len(list)
	_ = st.Snapshot("main.conf", []byte("version "+string(rune('A'+(HistoryKeep+4)%26))+strings.Repeat("x", HistoryKeep+4)), "ada")
	if list, _ = st.History("main.conf"); len(list) != before {
		t.Error("an unchanged text was kept again")
	}
	text, err := st.ReadVersion("main.conf", list[0].ID)
	if err != nil || !strings.HasPrefix(text, "version ") {
		t.Errorf("read: %q %v", text, err)
	}
	// Versions live outside sites.d, so no include reads them.
	if _, err := os.Stat(dir + "/history"); err != nil {
		t.Error(err)
	}
	if _, err := st.History("../x.conf"); err == nil {
		t.Error("a bad name was accepted")
	}
}
