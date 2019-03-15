package main

import "fmt"

type Animal struct {
	Type string
	Name string
}

func main() {
	a := Animal{Type: "Dog", Name: "Fido"}
	b := []Animal{a}
	c := make([]Animal, len(b))
	copy(c, b)

	copyOfA := c[0]
	copyOfA.Type = "Cat"

	fmt.Println(copyOfA)
	fmt.Println(a)
}
