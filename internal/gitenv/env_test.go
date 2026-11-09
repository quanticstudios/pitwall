package gitenv

import (
	"slices"
	"strings"
	"testing"
)

func TestClean(t *testing.T) {
	keep := []string{"PATH=/bin", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=user.name", "GIT_CONFIG_VALUE_0=test", "GIT_PAGER=cat"}
	env := slices.Clone(keep)
	for _, name := range strings.Fields(RepositoryVars) {
		env = append(env, name+"=/other/repository")
	}
	before := slices.Clone(env)
	if got := Clean(env); !slices.Equal(got, keep) {
		t.Errorf("Clean = %v, want %v", got, keep)
	}
	if !slices.Equal(env, before) {
		t.Error("Clean changed the caller's environment")
	}
}
