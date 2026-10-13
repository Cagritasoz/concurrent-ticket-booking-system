package main

import (
	"fmt"
	"time"
)

// cmd holds executables, the programs to actually run.

const defaultHoldTTL = 2 * time.Minute

func main() {
	fmt.Println(defaultHoldTTL)
}
