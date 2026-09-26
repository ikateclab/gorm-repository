package plugin

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNoGormRepositoryImports enforces section 3.1 of the design doc:
// the plugin/ tree must not import the surrounding gormrepository
// module, so it can be extracted to a standalone module unchanged.
//
// Test files are excluded from this check (-test=false), since tests
// may legitimately import gormrepository to verify integration.
func TestNoGormRepositoryImports(t *testing.T) {
	pkgs := []string{
		"github.com/ikateclab/gorm-repository/plugin",
		"github.com/ikateclab/gorm-repository/plugin/memory",
	}
	for _, pkg := range pkgs {
		t.Run(pkg, func(t *testing.T) {
			cmd := exec.Command("go", "list", "-test=false",
				"-f", `{{ range .Imports }}{{ println . }}{{ end }}`, pkg)
			out, err := cmd.Output()
			require.NoError(t, err, "go list failed: %s", string(out))
			for _, line := range strings.Split(string(out), "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				// Allow the module root only via the package's own self-reference (which won't appear here).
				if line == "github.com/ikateclab/gorm-repository" ||
					strings.HasPrefix(line, "github.com/ikateclab/gorm-repository/utils") ||
					(strings.HasPrefix(line, "github.com/ikateclab/gorm-repository/") &&
						!strings.HasPrefix(line, "github.com/ikateclab/gorm-repository/plugin")) {
					t.Errorf("forbidden import in %s: %s", pkg, line)
				}
			}
		})
	}
}
