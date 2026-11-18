//go:build !linux

package integration_test

import (
	"errors"
	"io"
)

func startGuardProc(string, []string, []string, io.Writer) (*guardProc, error) {
	return nil, errors.New("linux only")
}

func (p *guardProc) pid() int { return 0 }

func (p *guardProc) stop() error { return nil }

func sampleSupervisor(int) procSnap { return procSnap{} }

func clockTicks() float64 { return 100 }
