package grafana

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestV2VariableImportPreservesWideNumbersAndTopLevelDatasource(t *testing.T) {
	source := `{"apiVersion":"dashboard.grafana.app/v2beta1","metadata":{"name":"wide-vars"},"spec":{"title":"Variables","elements":{"keep":{"kind":"Panel","spec":{"id":1,"title":"Keep","vizConfig":{"group":"text","spec":{"options":{"content":"Keep"}}}}}},"layout":{"kind":"GridLayout","spec":{"items":[{"kind":"GridLayoutItem","spec":{"x":0,"y":0,"width":12,"height":8,"element":{"name":"keep"}}}]}},"variables":[{"kind":"QueryVariable","spec":{"name":"region","current":{"text":"East","value":"east","opaque":9007199254740993},"opaque":1e10000,"query":{"kind":"DataQuery","group":"mysql","datasource":{"name":"sql"},"spec":{"rawSql":"SELECT 'east' AS __value","opaque":9007199254740993}}}},{"kind":"GroupByVariable","group":"prometheus","datasource":{"name":"remote-prom"},"spec":{"name":"group","opaque":9007199254740993}},{"kind":"AdhocVariable","group":"prometheus","datasource":{"name":"remote-prom"},"spec":{"name":"filters","filters":[],"opaque":1e10000}}]}}`
	result, err := Import([]byte(source))
	require.NoError(t, err)
	require.Len(t, result.Dashboard.Variables, 3)
	assert.Contains(t, string(result.Dashboard.Variables[0].Config), "1e10000")
	assert.Contains(t, string(result.Dashboard.Variables[0].Config), "9007199254740993")
	assert.NotContains(t, string(result.Dashboard.Variables[0].Config), "9007199254740992")
	assert.Contains(t, string(result.Dashboard.Variables[0].Config), `"uid":"sql"`)
	assert.Equal(t, "groupby", result.Dashboard.Variables[1].Type)
	assert.Contains(t, string(result.Dashboard.Variables[1].Config), `"uid":"remote-prom"`)
	assert.Contains(t, string(result.Dashboard.Variables[2].Config), "1e10000")
	assert.Contains(t, string(result.Dashboard.Variables[2].Config), `"uid":"remote-prom"`)
}
