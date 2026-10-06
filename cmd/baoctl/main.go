package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"baoctl/internal/baoctl"
)

func main() {
	// Credentials and rendered files must never appear in a core dump.
	if err := syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{}); err != nil {
		fmt.Fprintln(os.Stderr, "无法禁用 core dump")
		os.Exit(1)
	}
	syscall.Umask(0077)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := baoctl.Run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "失败:", err)
		os.Exit(1)
	}
}
