//go:build !windows

package main

import (
	"context"

	klitkav1 "github.com/yevhen/klitka/proto/gen/go/klitka/v1"
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

func rewriteMountsForWsl(_ context.Context, _ *wslContext, mounts []*klitkav1.Mount) ([]*klitkav1.Mount, error) {
	return mounts, nil
}
