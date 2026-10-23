package access

import (
	"testing"

	"github.com/example/if3110-milestone1/internal/contract"
)

func TestLevelFromDB(t *testing.T) {
	cases := []struct {
		name string
		in int64
		want contract.AccessLevel
	}{
		{"no group", 0, contract.AccessNone},
		{"view only", 1, contract.AccessViewOnly},
		{"view edit", 2, contract.AccessViewEdit},
		{"unknown value fails closed", 3, contract.AccessNone},
		{"negative value fails closed", -1, contract.AccessNone},
	}
	
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := levelFromDB(c.in); got != c.want {
				t.Errorf("levelFromDB(%d) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
