package release

import "testing"

func TestImageRepository(t *testing.T) {
	got, err := ImageRepository("", "https://git.host.bzh", "pepe", "hello-world")
	if err != nil {
		t.Fatal(err)
	}
	if got != "git.host.bzh/pepe/hello-world" {
		t.Fatalf("got %q", got)
	}

	got, err = ImageRepository("git.host.bzh/pepe/other", "https://git.host.bzh", "pepe", "hello-world")
	if err != nil {
		t.Fatal(err)
	}
	if got != "git.host.bzh/pepe/other" {
		t.Fatalf("got %q", got)
	}
}
