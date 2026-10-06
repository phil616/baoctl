package baoctl

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestPasswordTerminal(t *testing.T) {
	for _, scenario := range []string{"immediate-input", "empty", "cancel", "eof"} {
		t.Run(scenario, func(t *testing.T) {
			master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
				t.Fatal(err)
			}
			number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
			if err != nil {
				t.Fatal(err)
			}
			slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer slave.Close()
			original, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			terminal := &terminal{slave, bufio.NewReader(slave), ctx}
			type result struct {
				password string
				err      error
			}
			done := make(chan result, 1)
			go func() {
				password, err := terminal.password(ctx)
				done <- result{password, err}
			}()
			var transcript strings.Builder
			for !strings.Contains(transcript.String(), "管理员密码（不回显）: ") {
				fds := []unix.PollFd{{Fd: int32(master.Fd()), Events: unix.POLLIN}}
				n, err := unix.Poll(fds, 1000)
				if err != nil || n == 0 {
					t.Fatal("password prompt did not arrive")
				}
				var buffer [256]byte
				n, err = master.Read(buffer[:])
				if err != nil {
					t.Fatal(err)
				}
				transcript.Write(buffer[:n])
			}
			hidden, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil || hidden.Lflag&(unix.ECHO|unix.ECHONL) != 0 {
				t.Fatal("echo must already be disabled when prompt is visible")
			}
			input := "fictional-password\n"
			switch scenario {
			case "cancel":
				cancel()
				input = ""
			case "empty":
				input = "\n"
			case "eof":
				input = string([]byte{original.Cc[unix.VEOF]})
			}
			if _, err := master.WriteString(input); err != nil {
				t.Fatal(err)
			}
			select {
			case r := <-done:
				if scenario == "immediate-input" {
					if r.err != nil || r.password != "fictional-password" {
						t.Fatal("password read failed")
					}
				} else if r.err == nil || r.password != "" {
					t.Fatal("expected failure without a password")
				}
			case <-time.After(4 * time.Second):
				t.Fatal("password read did not finish")
			}
			restored, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil || *restored != *original {
				t.Fatal("terminal state was not restored")
			}
		})
	}
}
