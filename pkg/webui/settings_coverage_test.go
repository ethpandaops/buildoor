package webui

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/config"
)

// dedicatedEditors lists the settings the UI changes through an endpoint of
// their own rather than the generic settings endpoint, because the write has
// side effects beyond storing the value. Each entry names the source file and
// the text that proves the editor exists there.
var dedicatedEditors = map[string]struct{ file, marker string }{
	config.KeyEPBSEnabled:            {"components/ConfigPanel.tsx", "epbs_enabled: !isActive"},
	config.KeyBuilderAPIEnabled:      {"components/BuilderAPIConfigPanel.tsx", "builder_api_enabled: !isActive"},
	config.KeyLifecycleEnabled:       {"components/BuilderInfo.tsx", "lifecycle_enabled: !lifecycleEnabled"},
	config.KeyBuilderKeysTargetCount: {"hooks/useBuilderKeys.ts", "/api/buildoor/builder-keys/target"},
}

// Every mutable setting must be editable from the WebUI. A registered setting
// without an editor is a trap: it takes effect, an operator can neither see why
// nor change it, and a similarly named setting that IS editable gets mistaken
// for it. A setting counts as editable when the frontend writes its canonical
// key through the generic settings endpoint, or has a dedicated editor above.
func TestEverySettingHasAnEditor(t *testing.T) {
	sources := make(map[string]string, 128)

	err := filepath.WalkDir("src", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() || (!strings.HasSuffix(path, ".ts") && !strings.HasSuffix(path, ".tsx")) {
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		rel, err := filepath.Rel("src", path)
		if err != nil {
			return err
		}

		sources[filepath.ToSlash(rel)] = string(content)

		return nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, sources, "no frontend sources found")

	for _, field := range config.Fields() {
		if editor, ok := dedicatedEditors[field.Key]; ok {
			require.Contains(t, sources[editor.file], editor.marker,
				"setting %q: dedicated editor not found in %s", field.Key, editor.file)

			continue
		}

		written := false

		for _, content := range sources {
			if strings.Contains(content, "'"+field.Key+"':") {
				written = true

				break
			}
		}

		require.True(t, written,
			"setting %q is registered but no WebUI editor writes it — add one, "+
				"or drop the setting from the registry", field.Key)
	}
}
