//go:build !windows

package main

import "context"

func holdPromiscuous(context.Context, string, func(map[string]any)) {}
