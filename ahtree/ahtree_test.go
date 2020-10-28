/*
Copyright 2019-2020 vChain, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package ahtree

import (
	"crypto/sha256"
	"os"
	"testing"

	"github.com/codenotary/merkletree"
	"github.com/stretchr/testify/require"
)

func TestAHtree(t *testing.T) {
	tree, err := Open("ahtree_test", DefaultOptions().SetSynced(false))
	require.NoError(t, err)
	defer os.RemoveAll("ahtree_test")

	var nodesUptoTests = []struct {
		n        uint64
		expected uint64
	}{
		{1, 1},
		{2, 3},
		{3, 5},
		{4, 8},
		{5, 10},
		{6, 13},
		{7, 16},
		{8, 20},
		{9, 22},
		{10, 25},
		{11, 28},
		{12, 32},
		{13, 35},
		{14, 39},
		{15, 43},
		{16, 48},
	}

	for _, tt := range nodesUptoTests {
		actual := nodesUpto(tt.n)
		require.Equal(t, tt.expected, actual)

		require.Equal(t, tt.expected, nodesUntil(tt.n)+uint64(levelsAt(tt.n))+1)
	}

	N := 1024

	for i := 1; i <= N; i++ {
		p := []byte{byte(i)}

		_, _, err := tree.Append(p)
		require.NoError(t, err)

		ri, err := tree.RootAt(uint64(i))
		require.NoError(t, err)

		r, err := tree.Root()
		require.NoError(t, err)
		require.Equal(t, r, ri)

		sz, err := tree.Size()
		require.NoError(t, err)
		require.Equal(t, uint64(i), sz)

		rp, err := tree.DataAt(uint64(i))
		require.NoError(t, err)
		require.Equal(t, p, rp)
	}

	_, err = tree.InclusionProof(2, 1)
	require.Error(t, ErrIllegalArguments, err)

	for i := 1; i <= N; i++ {
		for j := i; j <= N; j++ {
			proof, err := tree.InclusionProof(uint64(i), uint64(j))
			require.NoError(t, err)

			root, _ := tree.RootAt(uint64(j))

			h := sha256.Sum256([]byte{byte(i)})

			verifies := merkletree.Path(proof).VerifyInclusion(uint64(j)-1, uint64(i)-1, root, h)
			require.True(t, verifies)
		}
	}

	err = tree.Sync()
	require.NoError(t, err)

	err = tree.Close()
	require.NoError(t, err)

	err = tree.Sync()
	require.Error(t, ErrAlreadyClosed, err)

	err = tree.Close()
	require.Error(t, ErrAlreadyClosed, err)
}

func BenchmarkAppend(b *testing.B) {
	tree, _ := Open("ahtree_test", DefaultOptions().SetSynced(false))
	defer os.RemoveAll("ahtree_test")

	for i := 0; i < b.N; i++ {
		_, _, err := tree.Append([]byte{byte(i)})
		if err != nil {
			panic(err)
		}
	}
}
