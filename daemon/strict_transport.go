package daemon

type strictTransportEnforcer interface {
	AnchorID() string
	Close() error
}

type strictTransportFactoryFunc func(vmID string, proxyPort int) (strictTransportEnforcer, error)
type strictTransportPrepareFunc func() error

var strictTransportPrepare strictTransportPrepareFunc = func() error { return nil }
var strictTransportFactory strictTransportFactoryFunc = newStrictTransportEnforcer
