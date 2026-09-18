package main

import (
	"fmt"

	"github.com/davidbudnick/tools-nested-module-demo/internal/bar"
	"github.com/davidbudnick/tools-nested-module-demo/internal/web/foo"
)

func main() {
	fmt.Println(bar.Name())
	fmt.Println(foo.Page())
}
