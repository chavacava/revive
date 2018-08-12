package fixtures

func foo(a, b, c, d int) {
}

func bar(a, b int) {
}

func baz(a string, b int) {

}

func qux(a string, b int, c int, d string, e int64) {
	bar()
	baz()
}
