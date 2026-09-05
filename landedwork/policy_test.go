package landedwork_test

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/landedwork"
	"testing"
)

func TestGeneratedClassifierUsesGitPathsAndASCIICaseOnly(t *testing.T) {
	for _, test := range []struct {
		path      string
		generated bool
	}{
		{"data.locK", false}, {"dir\\go.sum", false}, {"dir/YARN.LOCK", true},
	} {
		assert.Equal(t, test.generated, landedwork.IsGeneratedPath(test.path), test.path)
	}
}

func TestCodePolicyPinsInclusionsAndExclusions(t *testing.T) {
	for _, tc := range []struct {
		path, reason string
		override     *bool
	}{
		{"src/main.GO", "included", nil}, {"infra/main.tf", "included", nil},
		{"shell.nix", "included", nil}, {"src/module.mli", "included", nil},
		{"build.gradle", "included", nil}, {"src/tool.groovy", "included", nil},
		{"src/main.zig", "included", nil}, {"infra/main.hcl", "included", nil},
		{"src/module.ml", "included", nil}, {"Makefile", "included", nil},
		{"makefile", "not_code", nil}, {"notes.md", "not_code", nil},
		{"config.json", "not_code", nil}, {"vendor/lib/main.go", "vendor", nil},
		{"src/node_modules/main.js", "vendor", nil}, {"third_party/main.c", "vendor", new(false)},
		{"src/schema.go", "generated", new(true)}, {"src/schema.go", "included", new(false)},
		{".terraform.lock.hcl", "generated", nil}, {".terraform.lock.hcl", "included", new(false)},
	} {
		t.Run(tc.path+tc.reason, func(t *testing.T) {
			assert.Equal(t, tc.reason, landedwork.ClassifyCodePath([]byte(tc.path), tc.override))
		})
	}
}

func TestPinnedAttributesMustAccountForEveryRequestedPath(t *testing.T) {
	attrs, err := landedwork.ParseGeneratedAttributes([]string{"a.go", "b.go", "c.go"}, []byte("a.go\x00linguist-generated\x00unset\x00b.go\x00linguist-generated\x00unspecified\x00c.go\x00linguist-generated\x00true\x00"))
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"a.go": false, "c.go": true}, attrs)
	for _, out := range []string{
		"", "a.go\x00linguist-generated\x00true", "a.go\x00other\x00true\x00",
		"b.go\x00linguist-generated\x00true\x00", "a.go\x00linguist-generated\x00surprise\x00",
		"a.go\x00linguist-generated\x00true\x00a.go\x00linguist-generated\x00unset\x00",
	} {
		_, err := landedwork.ParseGeneratedAttributes([]string{"a.go"}, []byte(out))
		assert.Error(t, err)
	}
}
