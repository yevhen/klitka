//go:build !windows

package main

import (
	"context"

	klitkavmv1 "github.com/klitkavm/klitkavm/proto/gen/go/klitkavm/v1"
)

type wslContext struct{}

func isWindows() bool {
	return false
}

func defaultTcpAddr() string {
	return ""
}

func ensureWslDaemon(_ context.Context, _ string) (*wslContext, error) {
	return nil, nil
}

func rewriteMountsForWsl(_ context.Context, _ *wslContext, mounts []*klitkavmv1.Mount) ([]*klitkavmv1.Mount, error) {
	return mounts, nil
}
