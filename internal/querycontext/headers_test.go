package querycontext

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHeadersAreIsolatedFromCallersAndConsumers(t *testing.T) {
	input := map[string]string{"FromAlert": "true"}
	ctx := WithHeaders(context.Background(), input)
	input["FromAlert"] = "changed"
	first := Headers(ctx)
	assert.Equal(t, "true", first["FromAlert"])
	first["FromAlert"] = "changed"
	assert.Equal(t, "true", Headers(ctx)["FromAlert"])
	assert.Nil(t, Headers(context.Background()))
}
