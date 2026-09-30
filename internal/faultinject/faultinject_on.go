//go:build faultinject

package faultinject

import "os"

// points are the enabled fault points, read once at start.
var points = parse(os.Getenv("PDA_FAULT"))

func hit(name string) { trigger(points, name, os.Stderr, os.Exit) }
