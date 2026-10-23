package install

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestFailureStopsDependentsAndRerunReobservesState(t *testing.T) {
	var calls []string
	fail := true
	steps := []Step{{"credentials", func(context.Context) error { calls = append(calls, "credentials"); return nil }}, {"bootstrap", func(context.Context) error {
		calls = append(calls, "bootstrap")
		if fail {
			return errors.New("network unavailable")
		}
		return nil
	}}, {"control plane", func(context.Context) error { calls = append(calls, "control"); return nil }}}
	err := Run(context.Background(), io.Discard, steps)
	if err == nil || !strings.Contains(err.Error(), "bootstrap") || !reflect.DeepEqual(calls, []string{"credentials", "bootstrap"}) {
		t.Fatalf("calls=%v err=%v", calls, err)
	}
	fail = false
	if err := Run(context.Background(), io.Discard, steps); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"credentials", "bootstrap", "credentials", "bootstrap", "control"}) {
		t.Fatal(calls)
	}
}
