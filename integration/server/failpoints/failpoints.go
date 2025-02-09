package failpoints

func ExampleFunc() string {
	// gofail: var ExampleString string
	// return ExampleString
	return "example"
}

func ExampleOneLineFunc() string {
	// gofail: var ExampleOneLine struct{}
	return "abc"
}

func ExampleLabelsFunc() string {
	i := 0
	// gofail: myLabel:
	s := ""
	for i < 5 {
		s = s + "i"
		i++
		for j := 0; j < 5; j++ {
			s = s + "j"
			// gofail: var ExampleLabels struct{}
			// continue myLabel
		}
	}
	return s
}
