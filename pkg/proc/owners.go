package proc

// Maps every process to the root among roots whose tree holds it, each root to itself
func Owners(roots []int) map[int]int {
	parents, err := parentsOf()
	if err != nil || len(roots) == 0 {
		return nil
	}
	isRoot := map[int]bool{}
	for _, r := range roots {
		if r > 0 {
			isRoot[r] = true
		}
	}
	out := map[int]int{}
	for pid := range parents {
		var path []int
		cur := pid
		for depth := 0; depth <= len(parents) && cur > 0; depth++ {
			root, owned := out[cur]
			if !owned && isRoot[cur] {
				root, owned = cur, true
			}
			if owned {
				out[cur] = root
				for _, p := range path {
					out[p] = root
				}
				break
			}
			path = append(path, cur)
			next, ok := parents[cur]
			if !ok {
				break
			}
			cur = next
		}
	}
	return out
}
