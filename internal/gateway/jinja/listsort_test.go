package jinja

import (
	"fmt"
	"math/rand/v2"
	"sort"
	"testing"
)

// Sorting random keys with duplicates gives the stable order forward and reversed, through every run and merge path
func TestListsort(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for _, n := range []int{0, 1, 2, 3, 7, 63, 64, 65, 100, 257, 1000, 5000} {
		for _, spread := range []int{2, 10, n + 1} {
			for _, reverse := range []bool{false, true} {
				t.Run(fmt.Sprintf("%d/%d/%v", n, spread, reverse), func(t *testing.T) {
					keys := make([]int, n)
					for i := range keys {
						keys[i] = rng.IntN(spread)
					}
					if spread == 2 && n > 10 {
						sort.Ints(keys[:n/2])
					}
					got, err := listsort(n, reverse, func(x, y int) (bool, error) { return keys[x] < keys[y], nil })
					if err != nil {
						t.Fatal(err)
					}
					want := make([]int, n)
					for i := range want {
						want[i] = i
					}
					if reverse {
						reverseInts(want)
					}
					sort.SliceStable(want, func(i, j int) bool { return keys[want[i]] < keys[want[j]] })
					if reverse {
						reverseInts(want)
					}
					for i := range want {
						if got[i] != want[i] {
							t.Fatalf("position %d holds key index %d, stable sort holds %d", i, got[i], want[i])
						}
					}
				})
			}
		}
	}
}
