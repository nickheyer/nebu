package requestcontrol

import (
	"errors"
	"fmt"
	"strings"

	"github.com/nickheyer/nebu/pkg/scheduling/framework"
)

var ErrTopologyCycle = errors.New("plugin dependencies form a cycle")

// A plugin and the plugins it must run before and after
type Registration struct {
	Plugin framework.Plugin
	Before []framework.TypedName
	After  []framework.TypedName
}

const stageCount = int(StageResponseComplete) + 1

type topology struct {
	stages [stageCount][][]framework.Plugin
}

// Whether a plugin takes part in a stage by the hook interface it implements
func inStage(p framework.Plugin, s Stage) bool {
	var ok bool
	switch s {
	case StagePreRequest:
		_, a := p.(PreRequestPlugin)
		_, b := p.(PreRequestMutator)
		ok = a || b
	case StagePostDecision:
		_, a := p.(PostDecisionPlugin)
		_, b := p.(PostDecisionMutator)
		ok = a || b
	case StageResponseHeaders:
		_, ok = p.(ResponseHeadersPlugin)
	case StageResponseBody:
		_, ok = p.(ResponseBodyPlugin)
	case StageResponseComplete:
		_, ok = p.(ResponseCompletePlugin)
	}
	return ok
}

// Builds the dependency graph, refusing duplicates, unknown names, stageless plugins and cycles, and levels every stage
func NewTopology(regs ...Registration) (Topology, error) {
	n := len(regs)
	index := make(map[framework.TypedName]int, n)
	for i, r := range regs {
		if r.Plugin == nil {
			return nil, fmt.Errorf("registration %d has no plugin", i)
		}
		name := r.Plugin.TypedName()
		if _, dup := index[name]; dup {
			return nil, fmt.Errorf("plugin %s is registered twice", name)
		}
		staged := false
		for s := range Stage(stageCount) {
			staged = staged || inStage(r.Plugin, s)
		}
		if !staged {
			return nil, fmt.Errorf("plugin %s implements no request control stage", name)
		}
		index[name] = i
	}
	succ := make([][]int, n)
	for i, r := range regs {
		for _, b := range r.Before {
			j, ok := index[b]
			if !ok {
				return nil, fmt.Errorf("plugin %s runs before unknown plugin %s", r.Plugin.TypedName(), b)
			}
			succ[i] = append(succ[i], j)
		}
		for _, a := range r.After {
			j, ok := index[a]
			if !ok {
				return nil, fmt.Errorf("plugin %s runs after unknown plugin %s", r.Plugin.TypedName(), a)
			}
			succ[j] = append(succ[j], i)
		}
	}
	order, err := kahn(regs, succ)
	if err != nil {
		return nil, err
	}
	reach := closure(order, succ)
	t := &topology{}
	for s := range Stage(stageCount) {
		var members []int
		for i, r := range regs {
			if inStage(r.Plugin, s) {
				members = append(members, i)
			}
		}
		t.stages[s] = levels(regs, members, reach)
	}
	return t, nil
}

func (t *topology) Batches(stage Stage) [][]framework.Plugin {
	if stage < 0 || int(stage) >= stageCount {
		return nil
	}
	return t.stages[stage]
}

// Orders every plugin so each follows its predecessors, naming the plugins left in a cycle
func kahn(regs []Registration, succ [][]int) ([]int, error) {
	indeg := make([]int, len(regs))
	for _, out := range succ {
		for _, j := range out {
			indeg[j]++
		}
	}
	var queue, order []int
	for i, d := range indeg {
		if d == 0 {
			queue = append(queue, i)
		}
	}
	for len(queue) > 0 {
		i := queue[0]
		queue = queue[1:]
		order = append(order, i)
		for _, j := range succ[i] {
			if indeg[j]--; indeg[j] == 0 {
				queue = append(queue, j)
			}
		}
	}
	if len(order) < len(regs) {
		var stuck []string
		for i, d := range indeg {
			if d > 0 {
				stuck = append(stuck, regs[i].Plugin.TypedName().String())
			}
		}
		return nil, fmt.Errorf("%w: %s", ErrTopologyCycle, strings.Join(stuck, ", "))
	}
	return order, nil
}

// Which plugins each plugin must precede, directly or through plugins of other stages
func closure(order []int, succ [][]int) [][]bool {
	reach := make([][]bool, len(order))
	for k := len(order) - 1; k >= 0; k-- {
		i := order[k]
		reach[i] = make([]bool, len(order))
		for _, j := range succ[i] {
			reach[i][j] = true
			for x, r := range reach[j] {
				reach[i][x] = reach[i][x] || r
			}
		}
	}
	return reach
}

// Groups the stage's plugins into Kahn levels, each level in registration order
func levels(regs []Registration, members []int, reach [][]bool) [][]framework.Plugin {
	indeg := make(map[int]int, len(members))
	for _, v := range members {
		indeg[v] = 0
		for _, u := range members {
			if reach[u][v] {
				indeg[v]++
			}
		}
	}
	var out [][]framework.Plugin
	placed := 0
	for placed < len(members) {
		var level []int
		for _, v := range members {
			if d, ok := indeg[v]; ok && d == 0 {
				level = append(level, v)
			}
		}
		batch := make([]framework.Plugin, len(level))
		for k, v := range level {
			batch[k] = regs[v].Plugin
			delete(indeg, v)
			for _, w := range members {
				if reach[v][w] {
					indeg[w]--
				}
			}
		}
		out = append(out, batch)
		placed += len(level)
	}
	return out
}
