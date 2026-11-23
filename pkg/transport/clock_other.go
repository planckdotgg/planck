//go:build !windows

package transport

import (
	"runtime"
	"time"
)

const spinThreshold = 200 * time.Microsecond

var epoch = time.Now()

func mono() int64    { return int64(time.Since(epoch)) }
func sleep(ns int64) { time.Sleep(time.Duration(ns)) }
func gosched()       { runtime.Gosched() }
