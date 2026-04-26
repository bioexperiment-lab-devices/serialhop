//go:build tools
// +build tools

package main

import (
	_ "github.com/jpillora/chisel/client"
	_ "go.bug.st/serial"
	_ "gopkg.in/yaml.v3"
)
