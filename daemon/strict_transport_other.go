//go:build !darwin

package daemon

func newStrictTransportEnforcer(_ string, _ int) (strictTransportEnforcer, error) {
	return nil, nil
}
