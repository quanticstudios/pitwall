// Package gitenv removes inherited repository selection from Git commands.
package gitenv

import (
	"slices"
	"strings"
)

// RepositoryVars is git rev-parse --local-env-vars without GIT_CONFIG_PARAMETERS
// and GIT_CONFIG_COUNT. Discovery settings and command-line config stay inherited.
const RepositoryVars = "GIT_DIR GIT_WORK_TREE GIT_COMMON_DIR GIT_INDEX_FILE " +
	"GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES " +
	"GIT_PREFIX GIT_IMPLICIT_WORK_TREE GIT_GRAFT_FILE GIT_SHALLOW_FILE " +
	"GIT_REPLACE_REF_BASE GIT_NO_REPLACE_OBJECTS GIT_CONFIG"

// Clean copies env without variables that select or redirect a repository.
func Clean(env []string) []string {
	names := strings.Fields(RepositoryVars)
	return slices.DeleteFunc(slices.Clone(env), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.Contains(names, name)
	})
}
