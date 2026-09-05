package platform_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
)

func TestEvidenceBudgetRejectsUnboundedWork(t *testing.T) {
	require := require.New(t)
	limits := platform.Budget{MaxRecords: 2, MaxNodes: 3, MaxBytes: 4, MaxOutputBytes: 8}
	_, err := platform.NewMeter(context.Background(), limits)
	require.ErrorIs(err, platform.ErrInvalidArgument)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err = platform.NewMeter(ctx, platform.Budget{})
	require.ErrorIs(err, platform.ErrInvalidArgument)
	budget, err := platform.NewMeter(ctx, limits)
	require.NoError(err)
	require.NoError(budget.Records(2))
	require.ErrorIs(budget.Records(1), platform.ErrPageLimit)
}

func TestEvidenceBudgetAccountsAcrossReads(t *testing.T) {
	require := require.New(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	budget, err := platform.NewMeter(ctx, platform.Budget{MaxRecords: 2, MaxNodes: 3, MaxBytes: 4, MaxOutputBytes: 8})
	require.NoError(err)
	first, err := budget.Read(ctx, strings.NewReader("abc"))
	require.NoError(err)
	assert.Equal(t, "abc", string(first))
	_, err = budget.Read(ctx, strings.NewReader("de"))
	require.ErrorIs(err, platform.ErrPageLimit)
}
