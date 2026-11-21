package utility

import (
	"context"
	"testing"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/constant"
	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rqp "github.com/timsolov/rest-query-parser"
)

func TestPointerIsToACopy(t *testing.T) {
	v := 3
	p := GetPointerToPrimitiveType(v)
	v = 4
	assert.Equal(t, 3, *p)
}

func TestNamespaceAndUserRoundTrip(t *testing.T) {
	ctx := WithUser(WithNamespace(context.Background(), "ns-1"), "u-1")
	ns, err := GetNamespaceID(ctx)
	require.NoError(t, err)
	user, err := GetUserID(ctx)
	require.NoError(t, err)
	assert.Equal(t, "ns-1", ns)
	assert.Equal(t, "u-1", user)
}

func TestReadersFollowTheKeyIndirection(t *testing.T) {
	ctx := context.WithValue(context.Background(), "tenant", "ns-2")
	ctx = context.WithValue(ctx, constant.CtxNamespaceKey, "tenant")
	ns, err := GetNamespaceID(ctx)
	require.NoError(t, err)
	assert.Equal(t, "ns-2", ns)

	// Without the indirection the direct key is read.
	direct := context.WithValue(context.Background(), constant.CtxUserID, "u-3")
	user, err := GetUserID(direct)
	require.NoError(t, err)
	assert.Equal(t, "u-3", user)
}

func TestMissingValuesAreHeaderErrors(t *testing.T) {
	_, err := GetNamespaceID(context.Background())
	assert.Equal(t, errors.RequiredHeaderMissingErrorCode, errors.CodeOf(err))
	_, err = GetUserID(nil) //nolint:staticcheck // nil context must not panic
	assert.Equal(t, errors.RequiredHeaderMissingErrorCode, errors.CodeOf(err))
}

func TestNamespaceFilterNeverWidensAQuery(t *testing.T) {
	_, err := AppendNamespaceFilterToQuery(context.Background(), rqp.New())
	assert.Error(t, err)

	q, err := AppendNamespaceFilterToQuery(WithNamespace(context.Background(), "ns-1"), nil)
	require.NoError(t, err)
	assert.Contains(t, q.Where(), "namespace = ?")
	assert.Equal(t, []interface{}{"ns-1"}, q.Args())
}
