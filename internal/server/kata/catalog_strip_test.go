package kata

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	katacatalog "go.kenn.io/forge/internal/kata"
)

// TestHandlerReportsCatalogTokenEnvNamesOnLoad pins the catalog-to-strip
// boundary: every successful catalog load reports the daemons' token_env
// names so credential stripping tracks catalog edits; failed loads
// report nothing.
func TestHandlerReportsCatalogTokenEnvNamesOnLoad(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var reported [][]string
	loadErr := error(nil)
	h := New(Deps{
		LoadCatalog: func() (katacatalog.Catalog, error) {
			if loadErr != nil {
				return katacatalog.Catalog{}, loadErr
			}
			return katacatalog.Catalog{Daemons: []katacatalog.Daemon{
				{TokenEnv: "KATA_PROD_TOKEN"},
			}}, nil
		},
		OnCatalogTokenEnvNames: func(names []string) {
			reported = append(reported, names)
		},
	})

	_, err := h.loadCatalog()
	require.NoError(err)
	assert.Equal([][]string{{"KATA_PROD_TOKEN"}}, reported)

	loadErr = errors.New("catalog unreadable")
	_, err = h.loadCatalog()
	require.Error(err)
	assert.Len(reported, 1, "undecodable loads must not report names")
}

// TestHandlerReportsDeclaredNamesFromRejectedCatalogs pins the
// decoded-but-invalid case: a rejected catalog's declared token names
// must still reach stripping.
func TestHandlerReportsDeclaredNamesFromRejectedCatalogs(t *testing.T) {
	var reported [][]string
	h := New(Deps{
		LoadCatalog: func() (katacatalog.Catalog, error) {
			return katacatalog.Catalog{Daemons: []katacatalog.Daemon{
					{TokenEnv: "KATA_REJECTED_TOKEN"},
				}},
				errors.New("catalog invalid")
		},
		OnCatalogTokenEnvNames: func(names []string) {
			reported = append(reported, names)
		},
	})
	_, err := h.loadCatalog()
	require.Error(t, err)
	assert.Equal(t, [][]string{{"KATA_REJECTED_TOKEN"}}, reported)
}
