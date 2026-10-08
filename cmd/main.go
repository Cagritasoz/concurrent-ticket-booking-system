package main

import (
	"fmt"
	"time"
)

const defaultHoldTTL = 2 * time.Minute

func main() {
	fmt.Println(defaultHoldTTL)
}
