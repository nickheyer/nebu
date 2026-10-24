package interceptors

// The header asking the gateway to add nothing of its own to a request, 1 or true turning it on
const BareHeader = "X-Nebu-Bare"

// Pins a request whose header asks for it bare, so no guard runs and no route tool server attaches
type Bare struct{}

func (Bare) Stage() Stage { return Inbound }

func (Bare) Guard() bool { return false }

func (Bare) Intercept(x *Exchange) (bool, error) {
	on, err := switched(BareHeader, x.Header.Get(BareHeader))
	if !on || err != nil {
		return false, err
	}
	x.Bare, x.Pinned = true, true
	return true, nil
}
