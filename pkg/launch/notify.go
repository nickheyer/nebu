package launch

// Wakes for changes of one file and the error that ended delivery
type notes struct {
	changed chan struct{}
	failed  chan error
}

func newNotes() notes {
	return notes{changed: make(chan struct{}, 1), failed: make(chan error, 1)}
}

// Queues one wake, coalescing with a wake not yet taken
func (n notes) wake() {
	select {
	case n.changed <- struct{}{}:
	default:
	}
}

// Reports the error that ends delivery, sent once by the watch goroutine before it returns
func (n notes) fail(err error) { n.failed <- err }
