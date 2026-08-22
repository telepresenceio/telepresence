package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPodOrdinal(t *testing.T) {
	ordinal, err := podOrdinal("checkout-splitter-12")
	require.NoError(t, err)
	require.Equal(t, 12, ordinal)
	_, err = podOrdinal("checkout")
	require.Error(t, err)
}
