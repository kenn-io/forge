package main

import (
	"go.kenn.io/middleman/tools/testifyhelpercheck"
	"golang.org/x/tools/go/analysis/singlechecker"
)

func main() {
	singlechecker.Main(testifyhelpercheck.Analyzer)
}
