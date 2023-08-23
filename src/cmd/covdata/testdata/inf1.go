package main

import "prog/dep"

func main() {
	x := 10
	if dep.Dep1() == 42 {
		dep.PDep(x)
	}
	dep.PDep(x + 10)
}
