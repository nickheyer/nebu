package launch

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Longest wait past the grace for the other ranks to go once one rank exits
const groupStopSlack = 30 * time.Second

// The processes of one launch's data parallel ranks supervised as one handle
type Group struct {
	members  []Handle
	log      *Log
	done     chan struct{}
	err      error
	stopping atomic.Bool
}

// Supervises the rank processes as one, their output merged into one log, the first exit stopping the rest within grace
func NewGroup(members []Handle, grace time.Duration) *Group {
	g := &Group{members: append([]Handle(nil), members...), log: NewLog(logCapacity), done: make(chan struct{})}
	for _, m := range g.members {
		m.Log().Tee(g.log)
	}
	go g.watch(grace)
	return g
}

// Waits for the first rank to exit, records why, stops every other rank, and closes once all are gone
func (g *Group) watch(grace time.Duration) {
	first := make(chan int, len(g.members))
	for i, m := range g.members {
		go func() {
			<-m.Done()
			first <- i
		}()
	}
	rank := <-first
	err := g.members[rank].Err()
	switch {
	case err != nil:
		g.err = fmt.Errorf("rank %d: %w", rank, err)
	case !g.stopping.Load():
		g.err = fmt.Errorf("rank %d exited with status 0 while the other ranks ran", rank)
	}
	ctx, cancel := context.WithTimeout(context.Background(), grace+groupStopSlack)
	defer cancel()
	var wg sync.WaitGroup
	for _, m := range g.members {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.Stop(ctx, grace)
			<-m.Done()
		}()
	}
	wg.Wait()
	g.log.Close()
	close(g.done)
}

// The rank processes in rank order
func (g *Group) Members() []Handle { return append([]Handle(nil), g.members...) }

// The process id of rank 0
func (g *Group) Pid() int { return g.members[0].Pid() }

// Closes once every rank exited and its output is read
func (g *Group) Done() <-chan struct{} { return g.done }

// The exit of the first rank to go, naming the rank, once Done is closed
func (g *Group) Err() error {
	select {
	case <-g.done:
		return g.err
	default:
		return nil
	}
}

// Every rank's output in the order it was read
func (g *Group) Log() *Log { return g.log }

// Reads everything every rank wrote so far into the log
func (g *Group) Sync() {
	for _, m := range g.members {
		m.Sync()
	}
}

// Stops every rank at once and waits for all of them until ctx ends
func (g *Group) Stop(ctx context.Context, grace time.Duration) error {
	g.stopping.Store(true)
	var wg sync.WaitGroup
	for _, m := range g.members {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.Stop(ctx, grace)
		}()
	}
	wg.Wait()
	select {
	case <-g.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
