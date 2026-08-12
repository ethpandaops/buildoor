package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// Every canonical settings key must be registered in the field registry.
// Declaring the constant without wiring the field compiles fine and looks
// complete, but the setting is then simply missing: the API rejects it as
// unknown and neither the UI nor the state-db can ever touch it.
func TestEverySettingsKeyIsRegistered(t *testing.T) {
	registered := make(map[string]struct{}, 64)
	for _, field := range Fields() {
		registered[field.Key] = struct{}{}
	}

	for name, key := range declaredSettingsKeys(t) {
		_, ok := registered[key]
		require.True(t, ok, "settings key %s (%q) is declared but not registered in Fields()", name, key)
	}
}

// Flag keys must be unique too: two fields sharing one viper flag would make
// CLI-change detection resolve them against each other's last-seen value.
func TestSettingsFlagKeysAreUnique(t *testing.T) {
	seen := make(map[string]string, 64)

	for _, field := range Fields() {
		previous, duplicate := seen[field.FlagKey]
		require.False(t, duplicate, "flag %q is used by both %s and %s", field.FlagKey, previous, field.Key)

		seen[field.FlagKey] = field.Key
	}
}

// declaredSettingsKeys parses settings_keys.go and returns every Key* constant
// as name -> value, so the check follows the source rather than a hand-kept
// list that would drift the same way the registry did.
func declaredSettingsKeys(t *testing.T) map[string]string {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), "settings_keys.go", nil, 0)
	require.NoError(t, err)

	keys := make(map[string]string, 64)

	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.CONST {
			continue
		}

		for _, spec := range genDecl.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok || len(valueSpec.Names) != 1 || len(valueSpec.Values) != 1 {
				continue
			}

			literal, ok := valueSpec.Values[0].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				continue
			}

			value, err := strconv.Unquote(literal.Value)
			require.NoError(t, err)

			keys[valueSpec.Names[0].Name] = value
		}
	}

	require.NotEmpty(t, keys, "no settings keys found in settings_keys.go")

	return keys
}
