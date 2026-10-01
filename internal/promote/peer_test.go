package promote

import "testing"

func TestParsePeers(t *testing.T) {
	got, err := ParsePeers([]string{"Billing", "postgres:Web", "billing"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"billing", "postgres:web"}
	if strings := FormatPeers(got); len(strings) != 2 || strings[0] != want[0] || strings[1] != want[1] {
		t.Fatalf("%v", FormatPeers(got))
	}
	if _, err := ParsePeers([]string{"redis:cache"}); err == nil {
		t.Fatal("expected unknown kind")
	}
}
