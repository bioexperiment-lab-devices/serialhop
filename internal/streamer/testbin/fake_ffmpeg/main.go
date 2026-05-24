// Package main implements a tiny ffmpeg stand-in for streamer tests.
//
// Behavior:
//   - Prints "fake_ffmpeg: started" to stderr immediately.
//   - If the env var FAKE_FFMPEG_EXIT_FAST=1, prints "exiting fast" to
//     stderr and exits 1 within ~50ms.
//   - If the env var FAKE_FFMPEG_IGNORE_SIGNALS=1, ignores SIGTERM /
//     CTRL_BREAK (Windows) and only exits on hard kill.
//   - Otherwise, sleeps until SIGTERM (Unix) / os.Interrupt (Windows)
//     and exits 0.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	fmt.Fprintln(os.Stderr, "fake_ffmpeg: started")
	if os.Getenv("FAKE_FFMPEG_EXIT_FAST") == "1" {
		if os.Getenv("FAKE_FFMPEG_DUMP_ENV") == "1" {
			fmt.Fprintf(os.Stderr, "fake_ffmpeg: env inherited=%s explicit=%s\n",
				yesNo(os.Getenv("STREAMER_TEST_INHERITED") == "yes"),
				yesNo(os.Getenv("STREAMER_TEST_EXPLICIT") == "yes"),
			)
		} else {
			fmt.Fprintln(os.Stderr, "fake_ffmpeg: exiting fast")
		}
		time.Sleep(50 * time.Millisecond)
		os.Exit(1)
	}
	if os.Getenv("FAKE_FFMPEG_DUMP_ENV") == "1" {
		fmt.Fprintf(os.Stderr, "fake_ffmpeg: env inherited=%s explicit=%s\n",
			yesNo(os.Getenv("STREAMER_TEST_INHERITED") == "yes"),
			yesNo(os.Getenv("STREAMER_TEST_EXPLICIT") == "yes"),
		)
	}
	ignore := os.Getenv("FAKE_FFMPEG_IGNORE_SIGNALS") == "1"

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, os.Interrupt)
	select {
	case <-sigCh:
		if ignore {
			// Stay alive; allow KILL to terminate us.
			select {}
		}
		fmt.Fprintln(os.Stderr, "fake_ffmpeg: clean exit")
		os.Exit(0)
	case <-time.After(30 * time.Second):
		// Safety: don't hang the test suite.
		os.Exit(2)
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
