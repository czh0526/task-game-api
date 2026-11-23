package metrics

import (
	"strings"
	"testing"
)

func TestIncValueResetAndHandler(t *testing.T) {
	Reset()
	Inc(LookupTotal, 3)
	Inc(LookupCacheHit, 2)
	if Value(LookupTotal) != 3 || Value(LookupCacheHit) != 2 {
		t.Fatalf("values: total=%d hit=%d", Value(LookupTotal), Value(LookupCacheHit))
	}
	var sb strings.Builder
	if err := Handler(&sb); err != nil {
		t.Fatal(err)
	}
	out := sb.String()
	for _, want := range []string{
		"# TYPE " + LookupTotal + " counter",
		LookupTotal + " 3",
		LookupCacheHit + " 2",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("handler output missing %q:\n%s", want, out)
		}
	}
	Reset()
	if Value(LookupTotal) != 0 {
		t.Fatal("reset did not zero the counter")
	}
}
